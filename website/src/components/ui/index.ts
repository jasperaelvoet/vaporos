// The site's primitives. Server components unless noted; import from '@/components/ui'.
//
//   Headline        width-is-temperature headings, fitted at their hottest cut
//   Button, LinkButton, buttonClass   hot · ghost · ash · ghost-ash; sm · md · lg
//   TextLink, SmartLink               links for content hrefs (base path aware)
//   CodeBlock (client), InlineCode, Kbd
//   Notice          info · warn · fault (the only cold thing, with hazard tape)
//   Chip            mono telemetry tags
//   Disclosure      a native <details> question and answer
//   Logo, LogoMark, Wordmark          the generated mark and the two-cut wordmark
//   Icon            the site's stroke icons (src/lib/icons.ts)
//   Rich, plain     the content markup renderer
export { Headline, type HeadlineProps, type HeadlineSize } from './headline';
export { Button, LinkButton, buttonClass, type ButtonSize, type ButtonVariant } from './button';
export { TextLink } from './text-link';
export { SmartLink, type SmartLinkProps } from './smart-link';
export { CodeBlock, InlineCode, Kbd } from './code';
export { Notice, type NoticeTone } from './notice';
export { Chip, type ChipTone } from './chip';
export { Disclosure } from './disclosure';
export { Logo, LogoMark, Wordmark, type Ground } from './logo';
export { Icon } from './icon';
export { Rich, plain, type RichComponents } from './rich';
