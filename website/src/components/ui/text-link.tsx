// An inline or standalone text link: underlined, in --strong (bone on dark
// grounds, ash on white-hot), thicker on hover. For content hrefs it picks
// next/link or <a> like SmartLink. External links stay in the same tab.
import { SmartLink, type SmartLinkProps } from './smart-link';

export function TextLink({ className = '', ...rest }: SmartLinkProps) {
  return (
    <SmartLink
      className={`font-semibold text-(--strong) underline decoration-[1.5px] underline-offset-[0.28em] hover:decoration-[2.5px] ${className}`}
      {...rest}
    />
  );
}
