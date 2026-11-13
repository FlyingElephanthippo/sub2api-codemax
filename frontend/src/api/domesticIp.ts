import { apiClient } from './client'

export interface DomesticIPGrant {
  id: number
  ip: string
  created_at: string
  updated_at: string
}

export interface DomesticIPState {
  enabled: boolean
  eligible: boolean
  paid_amount_cny: number
  remaining_amount_cny: number
  current_ip: string
  current_ip_valid: boolean
  entries: DomesticIPGrant[]
  max_ips: number
  daily_limit: number
  changes_used: number
  changes_remaining: number
}

const endpoint = '/user/ip-grants'

export const domesticIPAPI = {
  async get(): Promise<DomesticIPState> {
    return (await apiClient.get<DomesticIPState>(endpoint)).data
  },
  async add(expectedIP: string): Promise<DomesticIPState> {
    return (await apiClient.post<DomesticIPState>(endpoint, { expected_ip: expectedIP })).data
  },
  async replace(id: number, expectedIP: string): Promise<DomesticIPState> {
    return (await apiClient.put<DomesticIPState>(`${endpoint}/${id}`, { expected_ip: expectedIP })).data
  },
  async remove(id: number): Promise<DomesticIPState> {
    return (await apiClient.delete<DomesticIPState>(`${endpoint}/${id}`)).data
  }
}
