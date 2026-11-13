package repository

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/redis/go-redis/v9"
)

const (
	imageTaskKeyPrefix = "image_task:"
	// 任务本体按 id 存成散列，无法范围扫描，所以另建一个按创建时间排序的 ZSET
	// 索引来支撑列表查询。
	imageTaskIndexKeyPrefix = "image_task_index:"
	// 索引只在 API Key 维度保留最近的任务，避免为不活跃的 Key 无限堆积成员。
	imageTaskIndexMaxMembers = 200
)

type imageTaskStore struct {
	rdb *redis.Client
}

func NewImageTaskStore(rdb *redis.Client) service.ImageTaskStore {
	return &imageTaskStore{rdb: rdb}
}

func (s *imageTaskStore) Save(ctx context.Context, task *service.ImageTaskRecord, ttl time.Duration) error {
	data, err := json.Marshal(task)
	if err != nil {
		return err
	}
	if err := s.rdb.Set(ctx, imageTaskKey(task.ID), data, ttl).Err(); err != nil {
		return err
	}
	return s.index(ctx, task, ttl)
}

// index 把任务记进所属 API Key 的索引。索引写入失败不影响任务本体：
// 结果仍然可以按 id 查回，只是不会出现在列表里。
func (s *imageTaskStore) index(ctx context.Context, task *service.ImageTaskRecord, ttl time.Duration) error {
	key := imageTaskIndexKey(service.ImageTaskOwner{UserID: task.UserID, APIKeyID: task.APIKeyID})
	pipe := s.rdb.Pipeline()
	pipe.ZAdd(ctx, key, redis.Z{Score: float64(task.CreatedAt), Member: task.ID})
	// 排名是升序的，砍掉头部就是丢掉最旧的成员。
	pipe.ZRemRangeByRank(ctx, key, 0, -int64(imageTaskIndexMaxMembers)-1)
	pipe.Expire(ctx, key, ttl)
	_, err := pipe.Exec(ctx)
	return err
}

func (s *imageTaskStore) Get(ctx context.Context, id string) (*service.ImageTaskRecord, error) {
	data, err := s.rdb.Get(ctx, imageTaskKey(id)).Bytes()
	if err != nil {
		if err == redis.Nil {
			return nil, service.ErrImageTaskNotFound
		}
		return nil, err
	}
	var task service.ImageTaskRecord
	if err := json.Unmarshal(data, &task); err != nil {
		return nil, err
	}
	return &task, nil
}

func (s *imageTaskStore) List(ctx context.Context, owner service.ImageTaskOwner, limit int) ([]*service.ImageTaskRecord, error) {
	if limit <= 0 {
		limit = 100
	}
	ids, err := s.rdb.ZRevRange(ctx, imageTaskIndexKey(owner), 0, int64(limit-1)).Result()
	if err != nil || len(ids) == 0 {
		return nil, err
	}
	pipe := s.rdb.Pipeline()
	cmds := make([]*redis.StringCmd, len(ids))
	for i, id := range ids {
		cmds[i] = pipe.Get(ctx, imageTaskKey(id))
	}
	if _, err := pipe.Exec(ctx); err != nil && err != redis.Nil {
		return nil, err
	}
	tasks := make([]*service.ImageTaskRecord, 0, len(ids))
	for _, cmd := range cmds {
		data, err := cmd.Bytes()
		if err != nil {
			// 索引里可能残留已过期的成员（两次过期时间不完全同步），直接跳过。
			continue
		}
		var task service.ImageTaskRecord
		if err := json.Unmarshal(data, &task); err != nil {
			continue
		}
		tasks = append(tasks, &task)
	}
	return tasks, nil
}

func imageTaskKey(id string) string {
	return imageTaskKeyPrefix + strings.TrimSpace(id)
}

func imageTaskIndexKey(owner service.ImageTaskOwner) string {
	return fmt.Sprintf("%s%d:%d", imageTaskIndexKeyPrefix, owner.UserID, owner.APIKeyID)
}
