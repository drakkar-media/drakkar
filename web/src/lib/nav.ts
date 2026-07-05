import Gauge from '@lucide/svelte/icons/gauge';
import LibraryBig from '@lucide/svelte/icons/library-big';
import HardDriveDownload from '@lucide/svelte/icons/hard-drive-download';
import HeartPulse from '@lucide/svelte/icons/heart-pulse';
import Server from '@lucide/svelte/icons/server';
import FolderTree from '@lucide/svelte/icons/folder-tree';
import Settings2 from '@lucide/svelte/icons/settings-2';
import CalendarDays from '@lucide/svelte/icons/calendar-days';
import Users from '@lucide/svelte/icons/users';
import Captions from '@lucide/svelte/icons/captions';

// All nav items — shown in sidebar on desktop and in mobile drawer
export const navItems = [
  { href: '/dashboard',  label: 'Dashboard',  icon: Gauge },
  { href: '/library',    label: 'Library',    icon: LibraryBig },
  { href: '/calendar',   label: 'Calendar',   icon: CalendarDays },
  { href: '/downloads',  label: 'Downloads',  icon: HardDriveDownload },
  { href: '/subtitles',  label: 'Subtitles',  icon: Captions },
  { href: '/health',     label: 'Health',     icon: HeartPulse },
  { href: '/services',   label: 'Services',   icon: Server },
  { href: '/vfs',        label: 'Files',      icon: FolderTree },
  { href: '/users',      label: 'Users',      icon: Users },
  { href: '/settings',   label: 'Settings',   icon: Settings2 },
] as const;

// First 5 shown in mobile bottom bar; rest in drawer
export const mobilePrimaryItems = navItems.slice(0, 5);
