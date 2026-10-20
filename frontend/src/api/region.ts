import { apiClient } from "./client";

export interface RegionResponse {
  country_code: string;
  user_region_notice_enabled: boolean;
}

export async function getRegion(): Promise<RegionResponse> {
  const { data } = await apiClient.get<RegionResponse>("/region");
  return data;
}

export const regionAPI = {
  getRegion,
};

export default regionAPI;
