import { createRouter, createWebHistory } from 'vue-router'
import RegistryView from './views/RegistryView.vue'
import TaskTraceView from './views/TaskTraceView.vue'
import TopologyView from './views/TopologyView.vue'

export default createRouter({
  history: createWebHistory(),
  routes: [
    { path: '/', redirect: '/registry' },
    { path: '/registry', name: 'registry', component: RegistryView },
    { path: '/trace', name: 'trace', component: TaskTraceView },
    { path: '/topology', name: 'topology', component: TopologyView },
  ],
})
