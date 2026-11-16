import './theme.css';
import './base.css';
import { mount } from 'svelte';
import App from './App.svelte';
import { applyTheme } from './lib/state.svelte.js';

applyTheme();
mount(App, { target: document.getElementById('app') });
