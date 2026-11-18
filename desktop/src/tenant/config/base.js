// 获取API基础路径 - 优先使用外部配置
export const getApiBase = () => {
  // 检查是否存在外部配置
  if (typeof window !== 'undefined' && window.APP_CONFIG?.apiBase) {
    return window.APP_CONFIG.apiBase;
  }
  // 回退到构建时的环境变量
  return import.meta.env.VITE_API_BASE || '/nexus';
};

// 导出API基础路径
export const apiBase = getApiBase();
