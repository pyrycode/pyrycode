import { createRoot } from 'react-dom/client';
import { App } from './App';
import { title } from './title.json';
import './style.css';

const root = document.getElementById('root');
if (!root) throw new Error('Missing app root');
document.title = title;
createRoot(root).render(<App title={title} />);
