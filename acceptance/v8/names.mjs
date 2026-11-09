// Every route, field and selector the feature-page acceptance suites depend on, in one place: the page's API shape
// (see featv8page.go) and the markup featpage.js draws. A rename in either is a one-line change here.
export const R = {
  feature: (s) => `/api/term/feature?session=${encodeURIComponent(s)}`,
  events: (s, after, limit) => `/api/term/feature/events?session=${encodeURIComponent(s)}&after=${encodeURIComponent(after ?? '')}&limit=${limit ?? ''}`,
  seen: '/api/term/feature/seen', // {session, ids[]} -> {frontier}
  reviewed: '/api/term/feature/reviewed', // {session, id, rev} -> 200 | 409 {changed}
  message: '/api/term/feature/message', // {session, id, thread, text, anchor} -> {state} | 409 {thread}
  messageGet: (s, id) => `/api/term/feature/message?session=${encodeURIComponent(s)}&id=${encodeURIComponent(id)}`,
  ticketReply: '/api/term/feature/ticket/reply', // {session, ticket, option?, text?, rev}
  doc: '/api/term/feature/doc',
  bind: '/api/term/feature/bind',
  // agent (bearer) API
  aFeature: (s) => `/agent/feature?session=${encodeURIComponent(s)}`,
  aDoc: '/agent/feature/doc',
  aLog: '/agent/feature/log',
  aAssign: '/agent/feature/assign',
  aEvents: (s, after, limit) => `/agent/feature/events?session=${encodeURIComponent(s)}&after=${encodeURIComponent(after ?? '')}&limit=${limit ?? ''}`,
  sessions: (s) => `/api/term/feature/sessions?session=${encodeURIComponent(s)}`, // sessions + relations from assign events
  aThreadUpdate: '/agent/thread/update',
  aThreads: '/agent/threads',
};

// featFacts.page fields
export const PAGE_KEYS = ['observedAt', 'reconciledAt', 'events', 'seen', 'reviewed', 'sessions', 'relations', 'tickets', 'objectives', 'evidence', 'log', 'supervisor', 'manager'];
export const OPTIONAL_UNAUTHORED = ['activity']; // absent = "not tracked"

export const SECTIONS = ['Since you looked', 'Team timeline', 'Needs you', 'Plan and milestones', 'Code delivery', 'Work log'];

export const SEL = {
  section: (label) => `section[aria-label="${label}"]`,
  relstrip: '.relstrip',
  relCell: (repo, role) => `[data-relc="rel:${repo}:${role}"]`,
  laneBar: '.ss[data-sess]',
  step: (k) => `.steps li[data-key="${k}"]`,
  msSummary: (m) => `.ms[data-ms="${m}"] > summary`,
  msObjList: (m) => `.mso[data-mso="${m}"] li`,
  objective: (id) => `.dod > li[data-obj="${id}"]`,
  proofGo: '[data-proof-go]',
  objGo: '[data-obj-go]',
  needsItem: '.qi[data-q]',
  ticket: '#needs .tk',
  reply: '[data-reply]', send: '[data-send]', opt: '[data-opt]',
  prRow: 'tr.pr[data-open]', prRepo: '[data-prf]', prState: '[data-prs]', prFailing: '[data-prfail]',
  branchRow: '#graph [data-row]',
  logLine: '[data-log]', logFilter: (w) => `[data-wl="${w}"]`, logUnseen: '.lg.unseen', review: '[data-review]',
  chat: '.ctxchat[role=dialog]', chatText: '[data-ctext]', chatSend: '[data-csend]', chatRetry: '[data-cretry]',
  chatConfirm: '[data-cconfirm]', chatClose: '[data-cclose]', chatCtxToggle: '[data-ctxt]',
  metricCards: '.cd-top, section .ver:not(.d):not(.ev)' /* v7 metric cards; .d.ver/.ev.ver are legend/event dots */, floatingComment: '#cbtn',
};

// Strings that exist only in the design prototype's fixtures. None may appear on a real record's page.
export const FIXTURE_STRINGS = ['invoice-export', 'billing-app', 'billing-infra', 'load-tests', '3f9c2a1', 'a41d0e2', 'thr-ie-07',
  'req-32', 'fx#', 'Tester M3', 'Coder 2', 'ILLUSTRATIVE', 'simulated', 'Accountants export'];

// How the owner opens a feature page today (web/terms.js); update this if that changes.
export const openFeatureJS = (s) => `TV.feature = ${JSON.stringify(s)}; store.set('tv.feature', ${JSON.stringify(s)}); setView('term');`;
