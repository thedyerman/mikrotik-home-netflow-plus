import type { ReactNode, SVGProps } from "react";

// Minimal stroke icons on a 24px grid, sized by font-size via 1em.
function Icon({ children, ...rest }: SVGProps<SVGSVGElement> & { children: ReactNode }) {
  return (
    <svg viewBox="0 0 24 24" width="1em" height="1em" fill="none" stroke="currentColor" strokeWidth="1.75"
      strokeLinecap="round" strokeLinejoin="round" aria-hidden="true" {...rest}>
      {children}
    </svg>
  );
}

type P = SVGProps<SVGSVGElement>;

export const IconOverview = (p: P) => <Icon {...p}><path d="M3 12h4l3-8 4 16 3-8h4" /></Icon>;
export const IconFlows = (p: P) => <Icon {...p}><path d="M4 6h16M4 12h16M4 18h10" /></Icon>;
export const IconDevices = (p: P) => <Icon {...p}><rect x="3" y="4" width="18" height="12" rx="2" /><path d="M8 20h8M12 16v4" /></Icon>;
export const IconMap = (p: P) => <Icon {...p}><path d="M4 5c8 0 8 7 16 7M4 12h16M4 19c8 0 8-7 16-7" /></Icon>;
export const IconBell = (p: P) => <Icon {...p}><path d="M6 9a6 6 0 1 1 12 0c0 6 2.5 7 2.5 7h-17S6 15 6 9zM10 20a2 2 0 0 0 4 0" /></Icon>;
export const IconServer = (p: P) => <Icon {...p}><rect x="3" y="4" width="18" height="7" rx="2" /><rect x="3" y="13" width="18" height="7" rx="2" /><path d="M7 7.5h.01M7 16.5h.01" /></Icon>;
export const IconSun = (p: P) => <Icon {...p}><circle cx="12" cy="12" r="4" /><path d="M12 2v2M12 20v2M4.9 4.9l1.4 1.4M17.7 17.7l1.4 1.4M2 12h2M20 12h2M4.9 19.1l1.4-1.4M17.7 6.3l1.4-1.4" /></Icon>;
export const IconMoon = (p: P) => <Icon {...p}><path d="M20 14.5A8 8 0 0 1 9.5 4 8 8 0 1 0 20 14.5z" /></Icon>;
export const IconAuto = (p: P) => <Icon {...p}><circle cx="12" cy="12" r="9" /><path d="M12 3v18" /><path d="M12 7a5 5 0 0 1 0 10z" fill="currentColor" stroke="none" /></Icon>;
export const IconPause = (p: P) => <Icon {...p}><path d="M8 5v14M16 5v14" /></Icon>;
export const IconPlay = (p: P) => <Icon {...p}><path d="M7 5l12 7-12 7z" /></Icon>;
export const IconSearch = (p: P) => <Icon {...p}><circle cx="11" cy="11" r="7" /><path d="M20 20l-3.5-3.5" /></Icon>;
export const IconChevron = (p: P) => <Icon {...p}><path d="M9 6l6 6-6 6" /></Icon>;
export const IconDown = (p: P) => <Icon {...p}><path d="M12 5v14M6 13l6 6 6-6" /></Icon>;
export const IconUp = (p: P) => <Icon {...p}><path d="M12 19V5M6 11l6-6 6 6" /></Icon>;
export const IconLock = (p: P) => <Icon {...p}><rect x="5" y="11" width="14" height="9" rx="2" /><path d="M8 11V8a4 4 0 0 1 8 0v3" /></Icon>;
export const IconCheck = (p: P) => <Icon {...p}><path d="M5 12.5l4.5 4.5L19 7.5" /></Icon>;
export const IconX = (p: P) => <Icon {...p}><path d="M6 6l12 12M18 6L6 18" /></Icon>;
export const IconWarn = (p: P) => <Icon {...p}><path d="M12 3.5l9.5 16.5h-19z" /><path d="M12 10v4.5M12 17.5h.01" /></Icon>;
export const IconCritical = (p: P) => <Icon {...p}><path d="M8 3h8l5 5v8l-5 5H8l-5-5V8z" /><path d="M12 8v5M12 16h.01" /></Icon>;
export const IconInfo = (p: P) => <Icon {...p}><circle cx="12" cy="12" r="9" /><path d="M12 11v5.5M12 7.5h.01" /></Icon>;
export const IconEdit = (p: P) => <Icon {...p}><path d="M4 20h4L19 9l-4-4L4 16z" /><path d="M13.5 6.5l4 4" /></Icon>;
export const IconTable = (p: P) => <Icon {...p}><rect x="3" y="5" width="18" height="14" rx="2" /><path d="M3 10h18M3 14.5h18M9 5v14" /></Icon>;
export const IconChart = (p: P) => <Icon {...p}><path d="M4 19V5M4 19h16M8 15l3-4 3 2 5-7" /></Icon>;
export const IconCopy = (p: P) => <Icon {...p}><rect x="9" y="9" width="11" height="11" rx="2" /><path d="M5 15V6a2 2 0 0 1 2-2h9" /></Icon>;
export const IconRouter = (p: P) => <Icon {...p}><rect x="3" y="13" width="18" height="7" rx="2" /><path d="M7 16.5h.01M11 16.5h.01M12 13V9M8.5 6.5a5 5 0 0 1 7 0M6 4a8.5 8.5 0 0 1 12 0" /></Icon>;
export const IconDisplay = (p: P) => <Icon {...p}><rect x="3" y="5" width="18" height="12" rx="2" /><path d="M7 13l3-3 2.5 2L17 8M9 21h6" /></Icon>;
export const IconLogout = (p: P) => <Icon {...p}><path d="M15 4h3a2 2 0 0 1 2 2v12a2 2 0 0 1-2 2h-3M10 8l-4 4 4 4M6 12h10" /></Icon>;
