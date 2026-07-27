import type { AnchorHTMLAttributes, MouseEvent } from 'react';
import { navigateApp } from './historyStore';

type AppLinkProps = Omit<AnchorHTMLAttributes<HTMLAnchorElement>, 'href'> & { to: string };

export function AppLink({ to, onClick, target, ...props }: AppLinkProps) {
  const handleClick = (event: MouseEvent<HTMLAnchorElement>) => {
    onClick?.(event);
    if (event.defaultPrevented || event.button !== 0 || event.metaKey || event.ctrlKey || event.shiftKey || event.altKey || (target && target !== '_self') || props.download) return;
    event.preventDefault();
    navigateApp(to);
  };
  return <a {...props} href={to} target={target} onClick={handleClick} />;
}
