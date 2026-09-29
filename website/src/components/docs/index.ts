// The content pages' pieces (download, install guide, FAQ, 404). Server
// components unless noted; import from '@/components/docs'.
//
//   PageHead, SectionTitle   a content page's H1 and lead; its section headings
//   RequirementList          the requirements as a spec sheet
//   VerifySteps              the two download checks and the release key (server only)
//   GuideBlock               one block of the install guide
//   GuideToc (client)        the guide's contents, with each section's heat
//   InstallerScreen (client) the welcome screen while the installer waits
//   WizardSteps, StoreLinks  the installer's steps; where to get Moonlight
//   FaqList, FaqTopics       questions as <details>; the FAQ page by topic
//   OpenFromHash (client)    opens the <details> a #link points at
//   DeadScreen (client)      the 404's cold screen
export { PageHead, SectionTitle } from './page-head';
export { RequirementList } from './requirement-list';
export { GuideBlock } from './guide-block';
export { GuideToc, type TocItem } from './guide-toc';
export { InstallerScreen } from './installer-screen';
export { WizardSteps } from './wizard-steps';
export { StoreLinks } from './store-links';
export { FaqList, FaqTopics } from './faq-list';
export { OpenFromHash } from './open-from-hash';
export { DeadScreen } from './dead-screen';
