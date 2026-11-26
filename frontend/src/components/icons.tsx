// SPDX-License-Identifier: MIT
// icons — the single place for every UI icon in the app.
//
// Two families, kept strictly apart (2026-11-25 decision):
//  - Brand logos: the vendors' own marks, full colour, from
//    Simple Icons (CC0 1.0, simple-icons v12 paths, bundled locally —
//    never fetched from a CDN). Drime has no Simple Icons entry, so
//    its symbol is a vector trace of the official logo file from
//    drime.cloud (the combined logo webp, symbol half only), filled
//    with Drime brand green #56E16C. A local folder is not a brand:
//    it gets the flat-color-icons folder, like any other folder.
//  - Everything else: Iconify flat-color-icons (MIT, Icons8 set,
//    48×48 colourful glyphs). Imported per icon as data objects
//    from @iconify-icons/flat-color-icons and rendered with
//    @iconify/react's <Icon icon={data}> — fully offline, no CDN,
//    no API provider call; only the imported icons are bundled.
//    Two glyphs have no good match in the set, so they are small
//    neutral line SVGs drawn here (pencil for rename, chevron for
//    the advanced toggle) — never emoji, never another icon library.
//    Colourful icons carry their own fills, so they do not follow
//    currentColor and do not change colour on hover; hover feedback
//    is the background only.
//
// File-type icons are picked by extension, not by MIME: the table
// only ever sees a name. flat-color-icons has no PDF-specific glyph,
// so PDF shares `document` with the other text documents (closest
// match, not invented); slides use `video-projector` (a projector,
// the closest the set has to a presentation).
import { Icon } from '@iconify/react'
import folderIcon from '@iconify-icons/flat-color-icons/folder'
import openedFolderIcon from '@iconify-icons/flat-color-icons/opened-folder'
import fileIcon from '@iconify-icons/flat-color-icons/file'
import documentIcon from '@iconify-icons/flat-color-icons/document'
import dataSheetIcon from '@iconify-icons/flat-color-icons/data-sheet'
import videoProjectorIcon from '@iconify-icons/flat-color-icons/video-projector'
import imageFileIcon from '@iconify-icons/flat-color-icons/image-file'
import videoFileIcon from '@iconify-icons/flat-color-icons/video-file'
import audioFileIcon from '@iconify-icons/flat-color-icons/audio-file'
import packageIcon from '@iconify-icons/flat-color-icons/package'
import commandLineIcon from '@iconify-icons/flat-color-icons/command-line'
import synchronizeIcon from '@iconify-icons/flat-color-icons/synchronize'
import settingsIcon from '@iconify-icons/flat-color-icons/settings'
import cancelIcon from '@iconify-icons/flat-color-icons/cancel'
import fullTrashIcon from '@iconify-icons/flat-color-icons/full-trash'
import checkmarkIcon from '@iconify-icons/flat-color-icons/checkmark'
import refreshIcon from '@iconify-icons/flat-color-icons/refresh'
import servicesIcon from '@iconify-icons/flat-color-icons/services'
import infoIcon from '@iconify-icons/flat-color-icons/info'
import type { ProviderKind } from '../mygo'

// ---------------------------------------------------------------- UI icons
// flat-color-icons, 48×48 colourful glyphs, sized by the caller.

export function IconClose({ size = 16 }: { size?: number }) {
  return <Icon icon={cancelIcon} width={size} height={size} aria-hidden="true" />
}

export function IconTrash({ size = 16 }: { size?: number }) {
  return <Icon icon={fullTrashIcon} width={size} height={size} aria-hidden="true" />
}

export function IconRefresh({ size = 16 }: { size?: number }) {
  return <Icon icon={refreshIcon} width={size} height={size} aria-hidden="true" />
}

export function IconCheck({ size = 16 }: { size?: number }) {
  return <Icon icon={checkmarkIcon} width={size} height={size} aria-hidden="true" />
}

// SectionIcon — one flat-color-icons glyph per Settings section
// (same icon family as the file manager, kept consistent):
// general → settings, drives → folder, sync defaults →
// synchronize, Agent → services, about → info. Network has no
// globe/wifi in the installed flat-color-icons set, so it uses
// the neutral line globe (IconGlobe) below; cache likewise uses a
// neutral line database cylinder (IconDatabase).
export function SectionIcon({ section, size = 20 }: { section: 'general' | 'drives' | 'sync' | 'agent' | 'about' | 'network' | 'cache'; size?: number }) {
  if (section === 'network') return <IconGlobe size={size} />
  if (section === 'cache') return <IconDatabase size={size} />
  const data =
    section === 'general'
      ? settingsIcon
      : section === 'drives'
        ? folderIcon
        : section === 'sync'
          ? synchronizeIcon
          : section === 'agent'
            ? servicesIcon
            : infoIcon
  return <Icon icon={data} width={size} height={size} aria-hidden="true" style={{ flexShrink: 0 }} />
}

export function IconDatabase({ size = 20 }: { size?: number }) {
  return (
    <svg width={size} height={size} viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
      <ellipse cx="12" cy="5" rx="8" ry="3" />
      <path d="M4 5v14c0 1.66 3.58 3 8 3s8-1.34 8-3V5" />
      <path d="M4 12c0 1.66 3.58 3 8 3s8-1.34 8-3" />
    </svg>
  )
}

// Neutral line SVGs (no match in flat-color-icons): a pencil for
// rename, a chevron for the advanced toggle, a globe for the
// Network settings section, a database cylinder for the Cache
// section. 1.8px stroke, currentColor, same 24×24 grid habit as
// the rest of the UI.
export function IconGlobe({ size = 20 }: { size?: number }) {
  return (
    <svg width={size} height={size} viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
      <circle cx="12" cy="12" r="9" />
      <path d="M3 12h18" />
      <path d="M12 3a13.5 13.5 0 0 1 3.5 9 13.5 13.5 0 0 1-3.5 9 13.5 13.5 0 0 1-3.5-9 13.5 13.5 0 0 1 3.5-9z" />
    </svg>
  )
}

// Neutral line SVGs (no match in flat-color-icons): a pencil for
// rename and a chevron for the advanced toggle. 1.8px stroke,
// currentColor, same 24×24 grid habit as the rest of the UI.
export function IconPencil({ size = 15 }: { size?: number }) {
  return (
    <svg width={size} height={size} viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
      <path d="M4 20l1-4L16.5 4.5a2.12 2.12 0 0 1 3 3L8 19l-4 1z" />
      <path d="M14.5 6.5l3 3" />
    </svg>
  )
}

export function IconChevronDown({ size = 14, open = false }: { size?: number; open?: boolean }) {
  return (
    <svg
      width={size}
      height={size}
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="2"
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden="true"
      style={{ transform: open ? 'rotate(180deg)' : undefined, transition: 'transform 200ms cubic-bezier(0.22,1,0.36,1)' }}
    >
      <path d="M6 9l6 6 6-6" />
    </svg>
  )
}

export function NavIcon({ page, size = 18 }: { page: 'browse' | 'sync' | 'settings'; size?: number }) {
  const data = page === 'browse' ? folderIcon : page === 'sync' ? synchronizeIcon : settingsIcon
  return <Icon icon={data} width={size} height={size} aria-hidden="true" style={{ flexShrink: 0 }} />
}

// ---------------------------------------------------------------- file types
const DOC_EXTS = new Set(['doc', 'docx', 'txt', 'md', 'markdown', 'rtf', 'odt', 'pdf'])
const SHEET_EXTS = new Set(['xls', 'xlsx', 'csv', 'tsv', 'numbers', 'ods'])
const SLIDE_EXTS = new Set(['ppt', 'pptx', 'key', 'odp'])
const IMAGE_EXTS = new Set(['png', 'jpg', 'jpeg', 'gif', 'webp', 'svg', 'bmp', 'heic', 'heif', 'avif', 'ico', 'tif', 'tiff', 'raw'])
const VIDEO_EXTS = new Set(['mp4', 'mkv', 'mov', 'avi', 'webm', 'm4v', 'wmv', 'flv', 'mpg', 'mpeg'])
const AUDIO_EXTS = new Set(['mp3', 'wav', 'flac', 'aac', 'ogg', 'm4a', 'opus', 'wma'])
const ARCHIVE_EXTS = new Set(['zip', 'rar', '7z', 'tar', 'gz', 'bz2', 'xz', 'iso', 'dmg', 'pkg'])
const CODE_EXTS = new Set([
  'js', 'jsx', 'ts', 'tsx', 'py', 'go', 'rs', 'java', 'c', 'h', 'cpp', 'cc', 'cs',
  'css', 'scss', 'html', 'htm', 'json', 'yaml', 'yml', 'toml', 'xml', 'sh', 'bash', 'sql', 'php', 'rb', 'swift', 'kt',
])

function extOf(name: string): string {
  const i = name.lastIndexOf('.')
  if (i <= 0 || i === name.length - 1) return ''
  return name.slice(i + 1).toLowerCase()
}

/** FileTypeIcon — one flat-color-icons glyph per file kind, by
 *  extension. Folders get the yellow folder; unknown types get the
 *  generic `file`, never a blank. */
export function FileTypeIcon({ name, isDir, size = 18 }: { name: string; isDir: boolean; size?: number }) {
  let data = fileIcon
  if (isDir) {
    data = folderIcon
  } else {
    const ext = extOf(name)
    if (DOC_EXTS.has(ext)) data = documentIcon
    else if (SHEET_EXTS.has(ext)) data = dataSheetIcon
    else if (SLIDE_EXTS.has(ext)) data = videoProjectorIcon
    else if (IMAGE_EXTS.has(ext)) data = imageFileIcon
    else if (VIDEO_EXTS.has(ext)) data = videoFileIcon
    else if (AUDIO_EXTS.has(ext)) data = audioFileIcon
    else if (ARCHIVE_EXTS.has(ext)) data = packageIcon
    else if (CODE_EXTS.has(ext)) data = commandLineIcon
  }
  return <Icon icon={data} width={size} height={size} aria-hidden="true" style={{ flexShrink: 0 }} />
}

/** OpenedFolderIcon — the ".." parent row in the dual-pane picker. */
export function OpenedFolderIcon({ size = 18 }: { size?: number }) {
  return <Icon icon={openedFolderIcon} width={size} height={size} aria-hidden="true" style={{ flexShrink: 0 }} />
}

// ---------------------------------------------------------------- brand logos
// Simple Icons paths are 24×24, drawn in the vendor's brand colour.
// Google Drive's real mark is three-colour; Simple Icons ships the
// single-colour form, so it is filled with Drive blue #4285F4, the
// same single-colour treatment the library itself uses.
const BRAND: Record<string, { path: string; color: string; label: string }> = {
  dropbox: {
    color: '#0061FF',
    label: 'Dropbox',
    path: 'M6 1.807L0 5.629l6 3.822 6.001-3.822L6 1.807zM18 1.807l-6 3.822 6 3.822 6-3.822-6-3.822zM0 13.274l6 3.822 6.001-3.822L6 9.452l-6 3.822zM18 9.452l-6 3.822 6 3.822 6-3.822-6-3.822zM6 18.371l6.001 3.822 6-3.822-6-3.822L6 18.371z',
  },
  drive: {
    color: '#4285F4',
    label: 'Google Drive',
    path: 'M12.01 1.485c-2.082 0-3.754.02-3.743.047.01.02 1.708 3.001 3.774 6.62l3.76 6.574h3.76c2.081 0 3.753-.02 3.742-.047-.005-.02-1.708-3.001-3.775-6.62l-3.76-6.574zm-4.76 1.73a789.828 789.861 0 0 0-3.63 6.319L0 15.868l1.89 3.298 1.885 3.297 3.62-6.335 3.618-6.33-1.88-3.287C8.1 4.704 7.255 3.22 7.25 3.214zm2.259 12.653-.203.348c-.114.198-.96 1.672-1.88 3.287a423.93 423.948 0 0 1-1.698 2.97c-.01.026 3.24.042 7.222.042h7.244l1.796-3.157c.992-1.734 1.85-3.23 1.906-3.323l.104-.167h-7.249z',
  },
  onedrive: {
    color: '#0078D4',
    label: 'Microsoft OneDrive',
    path: 'M19.453 9.95q.961.058 1.787.468.826.41 1.442 1.066.615.657.966 1.512.352.856.352 1.816 0 1.008-.387 1.893-.386.885-1.049 1.547-.662.662-1.546 1.049-.885.387-1.893.387H6q-1.242 0-2.332-.475-1.09-.475-1.904-1.29-.815-.814-1.29-1.903Q0 14.93 0 13.688q0-.985.31-1.887.311-.903.862-1.658.55-.756 1.324-1.325.774-.568 1.711-.861.434-.129.85-.187.416-.06.861-.082h.012q.515-.786 1.207-1.413.691-.627 1.5-1.066.808-.44 1.705-.668.896-.229 1.845-.229 1.278 0 2.456.417 1.177.416 2.144 1.16.967.744 1.658 1.78.692 1.038 1.008 2.28zm-7.265-4.137q-1.325 0-2.52.544-1.195.545-2.04 1.565.446.117.85.299.405.181.792.416l4.78 2.86 2.731-1.15q.27-.117.545-.204.276-.088.58-.147-.293-.937-.855-1.705-.563-.768-1.319-1.318-.755-.551-1.658-.856-.902-.304-1.886-.304zM2.414 16.395l9.914-4.184-3.832-2.297q-.586-.351-1.23-.539-.645-.188-1.325-.188-.914 0-1.722.364-.809.363-1.412.978-.604.616-.955 1.436-.352.82-.352 1.723 0 .703.234 1.423.235.721.68 1.284zm16.711 1.793q.563 0 1.078-.176.516-.176.961-.516l-7.23-4.324-10.301 4.336q.527.328 1.13.504.604.175 1.237.175zm3.012-1.852q.363-.727.363-1.523 0-.774-.293-1.407t-.791-1.072q-.498-.44-1.166-.68-.668-.24-1.406-.24-.422 0-.838.1t-.815.252q-.398.152-.785.334-.386.181-.761.345Z',
  },
  cloudflare: {
    color: '#F38020',
    label: 'Cloudflare',
    path: 'M16.5088 16.8447c.1475-.5068.0908-.9707-.1553-1.3154-.2246-.3164-.6045-.499-1.0615-.5205l-8.6592-.1123a.1559.1559 0 0 1-.1333-.0713c-.0283-.042-.0351-.0986-.021-.1553.0278-.084.1123-.1484.2036-.1562l8.7359-.1123c1.0351-.0489 2.1601-.8868 2.5537-1.9136l.499-1.3013c.0215-.0561.0293-.1128.0147-.168-.5625-2.5463-2.835-4.4453-5.5499-4.4453-2.5039 0-4.6284 1.6177-5.3876 3.8614-.4927-.3658-1.1187-.5625-1.794-.499-1.2026.119-2.1665 1.083-2.2861 2.2856-.0283.31-.0069.6128.0635.894C1.5683 13.171 0 14.7754 0 16.752c0 .1748.0142.3515.0352.5273.0141.083.0844.1475.1689.1475h15.9814c.0909 0 .1758-.0645.2032-.1553l.12-.4268zm2.7568-5.5634c-.0771 0-.1611 0-.2383.0112-.0566 0-.1054.0415-.127.0976l-.3378 1.1744c-.1475.5068-.0918.9707.1543 1.3164.2256.3164.6055.498 1.0625.5195l1.8437.1133c.0557 0 .1055.0263.1329.0703.0283.043.0351.1074.0214.1562-.0283.084-.1132.1485-.204.1553l-1.921.1123c-1.041.0488-2.1582.8867-2.5527 1.914l-.1406.3585c-.0283.0713.0215.1416.0986.1416h6.5977c.0771 0 .1474-.0489.169-.126.1122-.4082.1757-.837.1757-1.2803 0-2.6025-2.125-4.727-4.7344-4.727',
  },
  s3: {
    color: '#569A31',
    label: 'Amazon S3',
    path: 'M20.913 13.147l.12-.895c.947.576 1.258.922 1.354 1.071-.16.031-.562.046-1.474-.176zm-2.174 7.988a.547.547 0 0 0-.005.073c0 .084-.207.405-1.124.768a10.28 10.28 0 0 1-1.438.432c-1.405.325-3.128.504-4.853.504-4.612 0-7.412-1.184-7.412-1.704a.547.547 0 0 0-.005-.073L1.81 5.602c.135.078.28.154.432.227.042.02.086.038.128.057.134.062.272.122.417.18l.179.069c.154.058.314.114.478.168.043.013.084.029.13.043.207.065.423.127.646.187l.176.044c.175.044.353.087.534.127a23.414 23.414 0 0 0 .843.17l.121.023c.252.045.508.085.768.122.071.011.144.02.216.03.2.027.4.053.604.077l.24.027c.245.026.49.05.74.07l.081.009c.275.022.552.04.83.056l.233.012c.21.01.422.018.633.025a33.088 33.088 0 0 0 2.795-.026l.232-.011c.278-.016.555-.034.83-.056l.08-.008c.25-.02.497-.045.742-.072l.238-.026c.205-.024.408-.05.609-.077.07-.01.141-.019.211-.03.261-.037.519-.078.772-.122l.111-.02c.215-.04.427-.082.634-.125l.212-.047c.186-.041.368-.085.546-.13l.166-.042c.225-.06.444-.122.654-.189.04-.012.077-.026.115-.038a10.6 10.6 0 0 0 .493-.173c.058-.021.114-.044.17-.066.15-.06.293-.12.43-.185.038-.017.079-.034.116-.052.153-.073.3-.15.436-.228l-.976 7.245c-2.488-.78-5.805-2.292-7.311-3a1.09 1.09 0 0 0-1.088-1.085c-.6 0-1.088.489-1.088 1.088 0 .6.488 1.089 1.088 1.089.196 0 .378-.056.537-.148 1.72.812 5.144 2.367 7.715 3.15zm-7.42-20.047c5.677 0 9.676 1.759 9.75 2.736l-.014.113c-.01.033-.031.067-.048.101-.015.028-.026.057-.047.087-.024.033-.058.068-.09.102-.028.03-.051.06-.084.09-.038.035-.087.07-.133.105-.04.03-.074.06-.119.091-.053.036-.116.071-.177.107-.05.03-.095.06-.15.09-.068.036-.147.073-.222.11-.059.028-.114.057-.177.085-.084.038-.177.074-.268.111-.068.027-.13.054-.203.082-.097.036-.205.072-.31.107-.075.026-.148.053-.228.079-.111.035-.233.069-.35.103-.085.024-.165.05-.253.073-.124.034-.258.065-.389.098-.093.022-.181.046-.278.068-.139.032-.287.061-.433.091-.098.02-.191.041-.293.06-.155.03-.32.057-.482.084-.1.018-.198.036-.302.052-.166.026-.342.048-.515.072-.11.014-.213.03-.325.044-.181.023-.372.041-.56.06-.11.012-.218.025-.332.036-.188.016-.386.029-.58.043-.122.009-.24.02-.364.028-.207.012-.422.02-.635.028-.12.005-.234.012-.354.016a35.605 35.605 0 0 1-2.069 0c-.12-.004-.234-.011-.352-.016-.214-.008-.43-.016-.637-.028-.122-.008-.238-.02-.36-.027-.195-.015-.394-.028-.584-.044-.11-.01-.215-.024-.324-.035-.19-.02-.384-.038-.568-.06l-.315-.044c-.176-.024-.355-.046-.525-.073-.1-.015-.192-.033-.29-.05-.167-.028-.335-.055-.494-.086-.096-.018-.183-.038-.276-.056-.151-.032-.305-.062-.45-.095-.09-.02-.173-.043-.26-.064-.138-.034-.277-.067-.407-.102-.082-.022-.157-.046-.235-.069a11.75 11.75 0 0 1-.368-.108c-.075-.024-.141-.049-.213-.073-.11-.037-.223-.075-.325-.113-.067-.025-.125-.051-.188-.077-.096-.038-.195-.076-.282-.115-.06-.027-.11-.054-.166-.08-.08-.039-.162-.077-.233-.116-.052-.028-.094-.055-.142-.084-.063-.038-.13-.075-.185-.113-.043-.029-.075-.058-.113-.086-.048-.037-.098-.073-.139-.11-.032-.029-.054-.057-.08-.087-.033-.035-.069-.07-.093-.104-.02-.03-.031-.058-.046-.086-.018-.035-.039-.068-.049-.102l-.015-.113c.076-.977 4.074-2.736 9.748-2.736zm12.182 12.124c-.118-.628-.84-1.291-2.31-2.128l.963-7.16a.531.531 0 0 0 .005-.073C22.16 1.581 16.447 0 11.32 0 6.194 0 .482 1.581.482 3.851a.58.58 0 0 0 .005.072L2.819 21.25c.071 2.002 5.236 2.75 8.5 2.75 1.805 0 3.615-.188 5.098-.531.598-.138 1.133-.3 1.592-.48 1.18-.467 1.789-1.053 1.813-1.739l.945-7.018c.557.131 1.016.197 1.389.197.54 0 .902-.137 1.134-.413a.956.956 0 0 0 .21-.804Z',
  },
  box: {
    color: '#0061D5',
    label: 'Box',
    path: 'M.959 5.523c-.54 0-.959.42-.959.899v7.549a4.59 4.59 0 004.613 4.494 4.717 4.717 0 004.135-2.457c.779 1.438 2.337 2.457 4.074 2.457 2.577 0 4.674-2.037 4.674-4.613.06-2.457-2.037-4.495-4.613-4.495-1.738 0-3.295.959-4.074 2.397-.78-1.438-2.338-2.397-4.135-2.397-1.079 0-2.038.36-2.817.899V6.422a.92.92 0 00-.898-.899zM17.602 9.26a.95.95 0 00-.704.158c-.36.3-.479.899-.18 1.318l2.397 3.116-2.396 3.115c-.3.42-.24.96.18 1.26.419.3 1.016.298 1.316-.122l2.039-2.636 2.096 2.697c.3.36.899.419 1.318.12.36-.3.42-.84.121-1.259l-2.338-3.115 2.338-3.057c.3-.419.298-1.018-.121-1.318-.48-.3-1.019-.24-1.318.18l-2.096 2.576-2.04-2.695c-.149-.18-.373-.3-.612-.338zM4.613 11.154c1.558 0 2.817 1.26 2.817 2.758 0 1.558-1.259 2.756-2.817 2.756-1.558 0-2.816-1.198-2.816-2.756 0-1.498 1.258-2.758 2.816-2.758zm8.27 0c1.558 0 2.816 1.26 2.816 2.758-.06 1.558-1.318 2.756-2.816 2.756-1.558 0-2.817-1.198-2.817-2.756 0-1.498 1.259-2.758 2.817-2.758Z',
  },
  mega: {
    color: '#D9272E',
    label: 'MEGA',
    path: 'M12 0C5.372 0 0 5.372 0 12s5.372 12 12 12 12-5.372 12-12S18.628 0 12 0zm6.23 16.244a.371.371 0 0 1-.373.372H16.29a.371.371 0 0 1-.372-.372v-4.828c0-.04-.046-.06-.08-.033l-3.32 3.32a.742.742 0 0 1-1.043 0l-3.32-3.32c-.027-.027-.08-.007-.08.033v4.828a.371.371 0 0 1-.372.372H6.136a.371.371 0 0 1-.372-.372V7.757c0-.206.166-.372.372-.372h1.076a.75.75 0 0 1 .525.22l4.13 4.13a.18.18 0 0 0 .26 0l4.13-4.13c.14-.14.325-.22.525-.22h1.075c.206 0 .372.166.372.372z',
  },
}

// Drime symbol, traced from the official logo on drime.cloud
// (2026-09 rebrand): four pinwheel petals around a small square gap.
// The brand kit says the mark is used as-is, never recoloured —
// here it is filled with the signature brand green #56E16C, the
// colour the mark itself carries on the Drime site and app.
const DRIME_PATHS = [
  'M8.49 10.39 L7.42 10.89 L6.43 11.13 L1.73 11.13 L0.99 10.8 L0.33 10.14 L0.0 9.32 L0.0 1.9 L0.33 1.07 L0.82 0.49 L1.98 0.0 L6.6 0.08 L8.41 0.74 L9.4 1.48 L10.06 2.23 L10.72 3.38 L11.05 4.37 L11.05 6.76 L10.72 7.84 L9.98 9.07 L8.49 10.39 Z',
  'M13.86 10.8 L13.11 9.98 L12.87 9.24 L12.87 8.41 L13.2 7.59 L20.45 0.33 L21.36 0.0 L21.94 0.0 L23.18 0.49 L23.59 0.91 L24.0 1.9 L24.0 9.32 L23.67 10.14 L23.01 10.8 L22.27 11.13 L14.6 11.13 L13.86 10.8 Z',
  'M2.8 24.0 L1.81 24.0 L1.15 23.75 L0.33 23.01 L0.0 22.19 L0.0 14.76 L0.41 13.77 L0.91 13.28 L1.9 12.87 L9.24 12.87 L10.23 13.28 L10.72 13.77 L11.13 14.76 L11.13 15.59 L10.72 16.58 L3.71 23.59 L2.8 24.0 Z',
  'M22.52 23.92 L14.68 24.0 L13.86 23.67 L13.28 23.09 L12.87 22.1 L12.95 17.32 L13.61 15.59 L14.27 14.68 L15.09 13.94 L16.25 13.28 L17.32 12.95 L19.55 12.95 L20.45 13.2 L21.44 13.69 L23.01 15.18 L23.67 16.41 L24.0 17.73 L24.0 22.19 L23.67 23.01 L23.01 23.67 L22.52 23.92 Z',
]

// pCloud has NO mark in Simple Icons (v12 / v16) or in Iconify's
// logos set — the only "pcloud" hit anywhere is UpCloud, a
// different product. So this is a hand-drawn APPROXIMATION of the
// pCloud mark, not the official logo: a cloud (Material's cloud
// path) in pCloud brand cyan #17BED0 with a white "P" — the shape
// pCloud's own logo carries. Replace it with the real mark if
// pCloud ever publishes one under a usable licence. (The bold P
// is the brand letterform, not UI text — the 400-weight rule is
// for interface copy.)
const PCLOUD_CLOUD_PATH =
  'M19.35 10.04C18.67 6.59 15.64 4 12 4 9.11 4 6.6 5.64 5.35 8.04 2.34 8.36 0 10.91 0 14c0 3.31 2.69 6 6 6h13c2.76 0 5-2.24 5-5 0-2.64-2.05-4.78-4.65-4.96z'

/** BrandLogo — the vendor's own mark at the left of a mount row,
 *  display only, never part of the click logic. S3 mounts show the
 *  Cloudflare mark when the endpoint is R2
 *  (*.r2.cloudflarestorage.com), otherwise the AWS S3 mark; the
 *  endpoint host comes from the backend as the display-only
 *  endpointHost field (no secret — it is the public endpoint URL). */
export function BrandLogo({ provider, endpointHost, size = 20 }: { provider: ProviderKind; endpointHost?: string; size?: number }) {
  const style = { flexShrink: 0 } as const
  if (provider === 'local') {
    return <Icon icon={folderIcon} width={size} height={size} aria-hidden="true" style={style} />
  }
  if (provider === 'drime') {
    return (
      <svg width={size} height={size} viewBox="0 0 24 24" fill="#56E16C" role="img" aria-label="Drime" data-brand="drime" style={style}>
        {DRIME_PATHS.map((d) => (
          <path key={d.slice(0, 12)} d={d} />
        ))}
      </svg>
    )
  }
  if (provider === 'pcloud') {
    return (
      <svg width={size} height={size} viewBox="0 0 24 24" role="img" aria-label="pCloud" data-brand="pcloud" style={style}>
        <path d={PCLOUD_CLOUD_PATH} fill="#17BED0" />
        <text x="12" y="16.5" textAnchor="middle" fontSize="11" fontWeight="700" fill="#ffffff" fontFamily="sans-serif">
          P
        </text>
      </svg>
    )
  }
  const key = provider === 's3' ? ((endpointHost ?? '').includes('r2.cloudflarestorage.com') ? 'cloudflare' : 's3') : provider
  const b = BRAND[key]
  if (!b) return <Icon icon={folderIcon} width={size} height={size} aria-hidden="true" style={style} />
  return (
    <svg width={size} height={size} viewBox="0 0 24 24" fill={b.color} role="img" aria-label={b.label} data-brand={key} style={style}>
      <path d={b.path} />
    </svg>
  )
}
