import { execFileSync } from 'node:child_process'
import { appendFileSync, readFileSync } from 'node:fs'
import { pathToFileURL } from 'node:url'

export function pullRequestAdvice({ title = '', body = '' }) {
  const advice = []
  if (!/\p{Script=Han}/u.test(title)) {
    advice.push('PR 标题未包含中文，建议补充中文标题，方便维护者阅读。')
  }
  const description = (body || '')
    .replace(/<!--[\s\S]*?(?:-->|$)/g, '')
    .replace(/^\s*(`{3,}|~{3,})[^\n]*\n[\s\S]*?^\s*\1\s*$/gm, '')
    .replace(/`[^`\n]*`/g, '')
    .replace(/^\s*#{1,6}\s+.*$/gm, '')
  if (!/\p{Script=Han}/u.test(description)) {
    advice.push('PR 正文未包含中文说明，建议补充中文的问题说明、改动内容和验证结果。')
  }
  return advice
}

export function commitAdvice(log) {
  return log.split('\n').filter(Boolean).flatMap(line => {
    const [sha, title] = line.split('\0')
    return /\p{Script=Han}/u.test(title || '')
      ? []
      : [`提交 ${sha} 的标题未包含中文，建议补充中文摘要。`]
  })
}

function git(...args) {
  return execFileSync('git', args, { encoding: 'utf8' }).trimEnd()
}

function main() {
  const event = JSON.parse(readFileSync(process.env.GITHUB_EVENT_PATH, 'utf8'))
  const advice = event.pull_request ? pullRequestAdvice(event.pull_request) : []
  const base = process.env.BASE_SHA
  const head = process.env.HEAD_SHA
  let revisions
  if (!base || /^0+$/.test(base)) {
    // 新分支不追溯其他远程分支已有的提交。
    const others = git('for-each-ref', '--format=%(objectname)', 'refs/remotes/origin/')
      .split('\n').filter(sha => sha && sha !== head)
    revisions = others.length ? [head, '--not', ...others] : ['-1', head]
  } else {
    revisions = [`${base}..${head}`]
  }
  advice.push(...commitAdvice(git('log', '--format=%h%x00%s', ...revisions)))
  // 使用固定提示和 Git 生成的提交哈希，避免把 PR 原文作为工作流指令输出。
  for (const message of advice) console.log(`::warning title=中文描述建议::${message}`)
  const summary = advice.length
    ? advice.map(message => `- ${message}`).join('\n')
    : '本次检查未发现需要补充中文的项目。'
  appendFileSync(process.env.GITHUB_STEP_SUMMARY,
    `## 中文描述建议\n\n${summary}\n\n以上仅为建议，不因缺少中文而阻断合并。\n`)
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) main()
