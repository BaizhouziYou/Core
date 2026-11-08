import assert from 'node:assert/strict'
import test from 'node:test'
import { commitAdvice, pullRequestAdvice } from './chinese-advice.mjs'

test('标题和正文分别提醒，允许中英文混用', () => {
  assert.equal(pullRequestAdvice({ title: 'Fix login', body: 'Fix a bug' }).length, 2)
  assert.equal(pullRequestAdvice({ title: '修复登录', body: 'Fix a bug' }).length, 1)
  assert.equal(pullRequestAdvice({ title: 'Fix login', body: '修复登录失败' }).length, 1)
  assert.deepEqual(pullRequestAdvice({ title: 'Fix 登录', body: '修复 WebUI 登录' }), [])
})

test('空正文和仅有模板标题、注释或代码的正文仍给出建议', () => {
  for (const body of [null, '', '## 改动说明\n<!-- 请填写中文 -->\nFix login',
    '```text\n中文\n```', '~~~text\n中文\n~~~', '`中文`']) {
    assert.equal(pullRequestAdvice({ title: '修复登录', body }).length, 1)
  }
})

test('识别繁体和扩展汉字', () => {
  assert.deepEqual(pullRequestAdvice({ title: '修復登入', body: '𠀀' }), [])
})

test('仅提醒缺少中文的提交，不回显不可信的标题', () => {
  const log = 'abc123\0修复登录\ndef456\0Fix login\nghi789\0::error::bad'
  assert.deepEqual(commitAdvice(log), [
    '提交 def456 的标题未包含中文，建议补充中文摘要。',
    '提交 ghi789 的标题未包含中文，建议补充中文摘要。',
  ])
  assert.deepEqual(commitAdvice(''), [])
})
