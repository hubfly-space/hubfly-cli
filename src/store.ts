import Conf from 'conf';
import { PROJECT_NAME } from './constants.js';

interface Schema {
  token: string;
}

const config = new Conf<Schema>({
  projectName: PROJECT_NAME,
});

export const getToken = (): string | undefined => {
  return config.get('token');
};

export const setToken = (token: string): void => {
  config.set('token', token);
};

export const deleteToken = (): void => {
  config.delete('token');
};
