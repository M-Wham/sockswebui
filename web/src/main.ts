import { mount } from 'svelte';
import './app.css';
import './theme/fonts.css';
import './theme/forest.css';
import { applyTheme, loadTheme } from './theme/theme';
import App from './App.svelte';

const target = document.getElementById('app');
if (!target) throw new Error('missing #app mount point');

applyTheme(loadTheme());

export default mount(App, { target });
