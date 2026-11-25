// Minimal i18n for the app shell (navigation, header, Settings
// page chrome). Only the shell is translated: the file browser,
// sync editor and dialogs stay in Chinese for now, and the Settings
// page says so instead of pretending everything is translated.
// Language is a persisted setting (zh | en); App owns the current
// value and applies it by re-rendering with the new strings.
export type Lang = 'zh' | 'en'

const DICT = {
  browse: { zh: '文件浏览', en: 'Browse' },
  sync: { zh: '同步任务', en: 'Sync Jobs' },
  settings: { zh: '设置', en: 'Settings' },
  searchPlaceholder: { zh: '搜索全部网盘… (Ctrl+K)', en: 'Search all drives… (Ctrl+K)' },
  newSync: { zh: '新建同步', en: 'New Sync' },
  drives: { zh: '网盘', en: 'Drives' },
  general: { zh: '通用', en: 'General' },
  syncDefaults: { zh: '同步默认', en: 'Sync Defaults' },
  agent: { zh: 'Agent', en: 'Agents' },
  about: { zh: '关于', en: 'About' },
  save: { zh: '保存', en: 'Save' },
  saved: { zh: '已保存', en: 'Saved' },
  cancel: { zh: '取消', en: 'Cancel' },
  delete: { zh: '删除', en: 'Delete' },
  confirmDelete: { zh: '确认删除？', en: 'Delete?' },
  edit: { zh: '编辑', en: 'Edit' },
  retry: { zh: '重试', en: 'Retry' },
  close: { zh: '关闭', en: 'Close' },
} as const satisfies Record<string, Record<Lang, string>>

export type I18nKey = keyof typeof DICT

export function t(lang: Lang, key: I18nKey): string {
  return DICT[key][lang]
}
