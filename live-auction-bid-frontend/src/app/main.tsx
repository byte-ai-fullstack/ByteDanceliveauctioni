import { createRoot } from 'react-dom/client';
import { QueryClientProvider } from '@tanstack/react-query';
import { adminQueryClient } from '../shared/api/queryClient';
import { App } from './App';
import './styles.css';
import './studio-tokens.css';

createRoot(document.getElementById('root')!).render(
  <QueryClientProvider client={adminQueryClient}>
    <App />
  </QueryClientProvider>,
);
