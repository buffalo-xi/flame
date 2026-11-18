// 节点输入变量的唯一实现。
//
// 背景：这套逻辑（校验键名 → 生成不重复的名字 → 增删改 → 全量快照）原先在 10 个面板里
// 各写了一遍，已经漂移出 4 种实现：前缀有 arg / variable 两种、有的预取 existingNames
// 有的每次现算、重名判断有的漏掉 outputs / targets、改名失败时有的回滚保存有的不回滚。
//
// 现在统一到这里。各面板保留自己的 handler 名字（模板不动），用别名接过去：
//   const { inputVariables, snapshot, ... } = useInputVariables({ ... })
//   const handleAddInputVariable = addInputVariable
import { ref } from 'vue'

const KEY_RE = /^[a-zA-Z][a-zA-Z0-9_]*$/

// 后缀顺序：add/delete/rename 之后一律回调 onChange（= 保存 config）。
// 改名失败也回调：让输入框按 state 重新渲染，避免界面上留着一个非法名字。
export function useInputVariables({
  // 面板自己持有 ref（沿用原来的声明位置），这里只是接过来操作
  inputVariables = ref({}),
  prefix = 'arg',
  reservedNames = () => [],
  onChange = () => {}
} = {}) {

  const validateInputKey = (key) => KEY_RE.test(key)

  const isInputVariableNameExists = (name, excludeName = '') => {
    if (name === excludeName) return false
    if (inputVariables.value[name] !== undefined) return true
    return reservedNames().includes(name)
  }

  const generateInputVariableName = () => {
    const taken = [...Object.keys(inputVariables.value), ...reservedNames()]
    let counter = 1
    while (taken.includes(prefix + counter)) counter++
    return prefix + counter
  }

  // 去掉 time 等界面字段，只留执行器要的四个字段
  const snapshot = () => {
    const inputs = {}
    Object.keys(inputVariables.value).forEach(name => {
      const v = inputVariables.value[name]
      inputs[name] = {
        source_type: v.source_type || 'argument',
        source_ref: v.source_ref || '',
        source_key: v.source_key || '',
        type: v.type || ''
      }
    })
    return inputs
  }

  const addInputVariable = () => {
    inputVariables.value[generateInputVariableName()] = {
      source_type: 'argument',
      source_ref: '',
      source_key: '',
      type: ''
    }
    onChange()
  }

  const updateInputVariableName = (oldName, value) => {
    const nextName = String(value ?? '').trim()
    const invalid = nextName === ''
      || nextName.includes(' ')
      || !validateInputKey(nextName)
      || nextName === oldName
      || isInputVariableNameExists(nextName, oldName)
    if (invalid) {
      // 保存一次让调用方用 state 重置输入框（否则非法名会留在 DOM 上）
      onChange()
      return
    }
    const next = { ...inputVariables.value }
    next[nextName] = { ...next[oldName] }
    delete next[oldName]
    inputVariables.value = next
    onChange()
  }

  const deleteInputVariable = (name) => {
    const next = { ...inputVariables.value }
    delete next[name]
    inputVariables.value = next
    onChange()
  }

  // 变量选择器的回调：参数是选择器回传的 JSON 字符串
  const applySelectedVariable = (name, value) => {
    try {
      const data = JSON.parse(value)
      inputVariables.value[name] = {
        ...inputVariables.value[name],
        source_type: data.source_type,
        source_ref: data.source_ref,
        source_key: data.source_key,
        type: data.variableType || ''
      }
      onChange()
    } catch (e) {
      console.error('Failed to parse variable value:', e)
    }
  }

  return {
    inputVariables,
    validateInputKey,
    generateInputVariableName,
    isInputVariableNameExists,
    snapshot,
    addInputVariable,
    updateInputVariableName,
    deleteInputVariable,
    applySelectedVariable
  }
}
