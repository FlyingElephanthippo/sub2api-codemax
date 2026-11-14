import { apiClient } from '../client'

export interface AdminDomesticIPGrant {
  id: number
  ip: string
  note: string
  created_at: string
  updated_at: string
}

export interface AdminDomesticIPGrantList {
  items: AdminDomesticIPGrant[]
  total: number
  page: number
  page_size: number
  enabled: boolean
  static_ips: string[]
}

const endpoint = '/admin/domestic-ip/grants'

export const adminDomesticIPAPI = {
  async list(page = 1, pageSize = 20, search = ''): Promise<AdminDomesticIPGrantList> {
    return (await apiClient.get<AdminDomesticIPGrantList>(endpoint, {
      params: { page, page_size: pageSize, search }
    })).data
  },
  async add(ip: string, note: string): Promise<AdminDomesticIPGrant> {
    return (await apiClient.post<AdminDomesticIPGrant>(endpoint, { ip, note })).data
  },
  async remove(id: number): Promise<void> {
    await apiClient.delete(`${endpoint}/${id}`)
  }
}
