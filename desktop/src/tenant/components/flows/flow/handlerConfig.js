// handler（响应处理脚本）配置的唯一实现。
//
// 背景：Http / Rpc / Mcp / LLM 四个面板各写了一份：5 个 ref + 5 个只为触发保存的
// handle* 方法 + config 读回与写出的两段样板。读回那段还有个共同的坑 ——
// 只写了 `if (cfg.handler) {...}`，没有 else 分支，于是从「有脚本的节点」切到
// 「没脚本的节点」时，界面仍显示上一个节点的脚本。
//
// 模板里的名字（handlerEnabled / handleHandlerToggle …）由本函数原样返回，面板不用改模板。
//
// 覆盖 Http / Rpc / Mcp 三个面板。LLM 没接：它的 handler 存在 llm.handler 里，
// 且「启用时若脚本为空就预填一段默认脚本」是刻意的行为，套用会改变语义。
import { ref } from 'vue'

// 语言下拉由各面板自己的 handlerLanguages 提供（彼此只差一个 filter，不必收进来）。
export function useHandlerConfig({
  defaultLang = 'lua',
  onChange = () => {}
} = {}) {
  const handlerEnabled = ref(false)
  const handlerScript = ref('')
  const handlerLang = ref(defaultLang)
  const handlerTimeoutEnabled = ref(false)
  const handlerTimeoutSeconds = ref(30)

  // 从 config.handler 读回；没有 handler 时重置为默认，避免残留上一个节点的内容
  const handlerLoad = (handler) => {
    handlerEnabled.value = !!handler
    handlerScript.value = handler?.script || ''
    handlerLang.value = handler?.lang || defaultLang
    handlerTimeoutEnabled.value = handler?.timeout?.enabled ?? false
    handlerTimeoutSeconds.value = handler?.timeout?.seconds || 30
  }

  // 写出 config.handler；关闭或脚本为空时返回 undefined，
  // 交给 deepMergeConfig 的 undefined 删除逻辑清掉残留的 handler
  const handlerBuild = () => (handlerEnabled.value && handlerScript.value)
    ? {
        script: handlerScript.value,
        lang: handlerLang.value,
        timeout: {
          enabled: handlerTimeoutEnabled.value,
          seconds: handlerTimeoutSeconds.value
        }
      }
    : undefined

  const handleHandlerToggle = () => onChange()
  const handleHandlerLangChange = () => onChange()
  const handleHandlerScriptChange = (value) => {
    handlerScript.value = value
    onChange()
  }
  const handleHandlerTimeoutToggle = () => onChange()
  const handleHandlerTimeoutChange = (value) => {
    const num = parseInt(value)
    if (!isNaN(num) && num > 0) handlerTimeoutSeconds.value = num
    onChange()
  }

  return {
    handlerEnabled,
    handlerScript,
    handlerLang,
    handlerTimeoutEnabled,
    handlerTimeoutSeconds,
    handlerLoad,
    handlerBuild,
    handleHandlerToggle,
    handleHandlerLangChange,
    handleHandlerScriptChange,
    handleHandlerTimeoutToggle,
    handleHandlerTimeoutChange
  }
}
