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

export interface Region {
  id: string;
  name: string;
  location: string;
  available: boolean;
  createdAt: string;
  primaryIP: string;
  primaryProvider: string;
}

export interface Project {
  id: string;
  name: string;
  networkName: string;
  status: string;
  ownerId: string;
  regionId: string;
  createdAt: string;
  updatedAt: string;
  monthlyCost: string;
  hourlyCost: string;
  totalConsumed: string;
  budget: string;
  spentAmount: string;
  region: Region;
  role: string;
}

export interface Container {
  id: string;
  name: string;
  projectId: string;
  tier: string;
  status: string;
  source: {
    type: string;
    template?: string;
    dockerImage?: string;
    gitRepository?: string;
  };
  resources: {
    cpu: number;
    ram: number;
    storage: number;
  };
  runtime: {
    is24x7: boolean;
    autoScale: boolean;
    autoSleep: boolean;
  };
  networking: {
    ports: any[];
  };
  dockerContainerId: string;
  createdAt: string;
  updatedAt: string;
}

export interface ProjectDetails {
  containers: Container[];
}

export const fetchProjects = async (token: string): Promise<Project[]> => {
  try {
    const response = await fetch(`${API_HOST}/api/projects`, {
      headers: {
        Authorization: `Bearer ${token}`,
      },
    });

    if (!response.ok) {
      throw new ApiError('Failed to fetch projects', response.status);
    }

    const data = await response.json();
    return (data as { projects: Project[] }).projects;
  } catch (error) {
    if (error instanceof ApiError) {
      throw error;
    }
    throw new Error('Network error or invalid response');
  }
};

export const fetchProject = async (token: string, projectId: string): Promise<ProjectDetails> => {
  try {
    const response = await fetch(`${API_HOST}/api/projects/${projectId}`, {
      headers: {
        Authorization: `Bearer ${token}`,
      },
    });

    if (!response.ok) {
      throw new ApiError('Failed to fetch project details', response.status);
    }

    const data = await response.json();
    return data as ProjectDetails;
  } catch (error) {
    if (error instanceof ApiError) {
      throw error;
    }
    throw new Error('Network error or invalid response');
  }
};

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
