import { existsSync, writeFileSync } from 'node:fs'

if (existsSync('node_modules')) {
  writeFileSync('node_modules/go.mod', 'module ignore_node_modules\n')
}
