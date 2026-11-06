import { defineDocsConfig } from '@tavric-labs/docs-theme/config'

export default defineDocsConfig({
  name: 'nginx-inspector',
  repo: 'tavric-labs/nginx-inspector',
  siteUrl: 'https://nginx-inspector.tavric-labs.net',
  description: 'A native execution inspector for nginx: routing, filters, upstreams, buffers and event state.',
  nav: [
    { text: 'Manual', link: '/quick-start' },
    { text: 'Internals', link: '/native-observations' },
    { text: 'Architecture', link: '/architecture' },
    { text: 'Releases', link: '/releases' }
  ],
  sidebar: [
    { text: 'OPERATIONS MANUAL', items: [
      { text: 'Overview', link: '/overview' }, { text: 'Installation', link: '/install' },
      { text: 'First capture', link: '/quick-start' }, { text: 'Capture control', link: '/capture' },
      { text: 'Desktop inspector', link: '/dashboard' }, { text: 'Workers and concurrency', link: '/workers' },
      { text: 'Worker anatomy', link: '/worker-anatomy' },
      { text: 'Proxy, cache and controls', link: '/traffic-controls' }, { text: 'Traffic and overhead', link: '/performance' }, { text: 'Resource footprint', link: '/resources' }
    ]},
    { text: 'NGINX INTERNALS', items: [
      { text: 'The nginx engine', link: '/nginx-engine' },
      { text: 'Kernel to worker', link: '/kernel-to-worker' },
      { text: 'Native observations', link: '/native-observations' },
      { text: 'Reading the evidence', link: '/reading-events' },
      { text: 'Coverage and limits', link: '/coverage' }, { text: 'Architecture', link: '/architecture' }
    ]},
    { text: 'ENGINEERING', items: [
      { text: 'Deployment', link: '/deployment' }, { text: 'Development', link: '/development' },
      { text: 'Research and references', link: '/research' },
      { text: 'Binary releases', link: '/releases' }, { text: 'Troubleshooting', link: '/troubleshooting' }
    ]}
  ]
})
