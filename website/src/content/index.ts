// All site copy and facts, typed. Import from '@/content'.
//
// Everything here is plain data (no Node APIs), so it works in server and
// client components. The two things only the build can know come from
// src/lib: the release (release.ts, use-latest-release.ts) and the release
// key (key.ts, passed to verifySteps() / releaseKeyCard()).
export * from './types';
export * from './site';
export * from './home';
export * from './features';
export * from './how-it-works';
export * from './requirements';
export * from './install';
export * from './faq';
export * from './download';
export * from './verify';
export * from './not-found';
export * from './mock';
