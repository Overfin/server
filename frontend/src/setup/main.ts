import '@overfin/ui/theme.css';
import '@overfin/ui/admin.css';
import '../app.css';
import { mount } from 'svelte';
import Setup from './Setup.svelte';

mount(Setup, { target: document.getElementById('app')! });
