import { API_HOST } from './constants.js';

export interface User {
  id: string;
  name: string;
  email: string;
  image: string;
}

export class ApiError extends Error {
  status: number;
  constructor(message: string, status: number) {
    super(message);
    this.status = status;
  }
}

export const fetchWhoAmI = async (token: string): Promise<User> => {
  try {
    const response = await fetch(`${API_HOST}/api/auth/whoami`, {
      headers: {
        Authorization: `Bearer ${token}`,
      },
    });

    if (!response.ok) {
      throw new ApiError('Failed to fetch user', response.status);
    }

    const data = await response.json();
    return data as User;
  } catch (error) {
    if (error instanceof ApiError) {
      throw error;
    }
    // specific handling for fetch network errors if needed
    throw new Error('Network error or invalid response');
  }
};
