import { spawnSync } from 'node:child_process'
import { copyFileSync, cpSync, mkdirSync, mkdtempSync, rmSync, writeFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'

const root = dirname(fileURLToPath(import.meta.url))
const dev = join(root, 'dev')
const env = { ...process.env, CGO_ENABLED: '0' }

function run(command, args, cwd) {
  if (process.platform === 'win32' && command === 'npm') {
    // npm 的参数均为下方固定命令，不拼接用户输入。
    args = ['/d', '/s', '/c', `npm ${args.join(' ')}`]
    command = 'cmd.exe'
  }
  const result = spawnSync(command, args, {
    cwd,
    env,
    stdio: 'inherit',
  })
  if (result.error || result.status !== 0) {
    throw new Error(`${command} 执行失败（${result.status ?? result.error?.message}）`)
  }
}

run('npm', ['ci', '--prefer-offline', '--no-audit', '--no-fund'], join(dev, 'web'))
run('npm', ['run', 'build'], join(dev, 'web'))
mkdirSync(join(dev, 'bin'), { recursive: true })
const target = env.GOOS || (process.platform === 'win32' ? 'windows' : process.platform)
const output = join('bin', target === 'windows' ? 'easybotce.exe' : 'easybotce')
run('go', ['build', '-trimpath', '-o', output, './cmd/easybot'], dev)

const arch = env.GOARCH || ({ x64: 'amd64', ia32: '386' }[process.arch] || process.arch)
const name = `easybotce-${target}-${arch}`
const release = join(root, 'release')
if (!/^easybotce-[a-z0-9]+-[a-z0-9]+$/.test(name)) throw new Error('无效的目标平台')
mkdirSync(release, { recursive: true })
const folder = mkdtempSync(join(release, '.package-'))
try {
  copyFileSync(join(dev, output), join(folder, target === 'windows' ? 'easybotce.exe' : 'easybotce'))
  copyFileSync(join(dev, 'internal', 'config', 'appsettings.example.json'), join(folder, 'appsettings.json'))
  cpSync(join(dev, 'web', 'dist'), join(folder, 'web', 'dist'), { recursive: true })
  for (const file of ['LICENSE', 'AUTHORS.md']) copyFileSync(join(root, file), join(folder, file))
  writeFileSync(join(folder, '使用说明.txt'), [
    'EasybotCE 便携包',
    target === 'windows' ? '解压到可写目录后，双击 easybotce.exe 启动，运行期间保持窗口打开。' : '解压后运行 ./easybotce；若缺少执行权限，先执行 chmod +x easybotce。',
    '浏览器打开 http://127.0.0.1:5001，默认本机监听直接进入管理台，无需注册或登录。',
    '修改同目录 appsettings.json 中 Kestrel.Endpoints.web_api.Url 可调整 WebUI 地址，保存后重启生效。',
    'ServerOptions 是 Bridge 配置，默认端口 26991；当前 Bridge 运行时尚未实现。',
    '迁移时可复制原版 appsettings.json，已有文件不会被重写；旧端口按原值保留。',
    '非本地监听可直接使用 HTTP；网页只需输入登录 Token，不需要用户名或独立 API Key。',
    '首次自动生成 12 位 Token，在控制台显示一次；token.txt 保存哈希，后续沿用。已有原版 token.txt 可直接复制到 exe 旁。',
    '双击启动失败时，窗口显示错误原因并等待回车关闭。',
    '请保留包内 web 目录。运行数据自动保存到 appdata 目录。',
    '升级前停止程序、备份并保留 appsettings.json、token.txt 和 appdata，再替换程序和 web 目录。',
    '默认使用 SQLite，无需安装开发环境或数据库服务器。',
  ].join('\n') + '\n', 'utf8')

  if (target === 'windows' && process.platform === 'win32') {
    env.EASYBOT_PACKAGE_DIR = folder
    env.EASYBOT_ARCHIVE_PATH = join(release, `${name}.zip`)
    run('powershell.exe', ['-NoProfile', '-NonInteractive', '-Command', 'Compress-Archive -Path (Join-Path $env:EASYBOT_PACKAGE_DIR "*") -DestinationPath $env:EASYBOT_ARCHIVE_PATH -Force'], root)
  } else {
    run('tar', ['-czf', join(release, `${name}.tar.gz`), '-C', folder, '.'], root)
  }
} finally {
  // 只删除本次新建的临时打包目录，已解压的程序及数据不受影响。
  if (dirname(folder) !== release) throw new Error('无效的清理目录')
  rmSync(folder, { recursive: true, force: true })
}
console.log(`便携包已生成：${release}`)
