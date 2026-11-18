// 导入API基础路径
import { apiBase } from './base.js';

// 服务模块API配置
const serviceApi = {
  // 智能体服务API
  agent: {
    base: `${apiBase}/service/v1/agent/`,
    execute: `${apiBase}/service/v1/flow/rpc`,
    standalone: `${apiBase}/service/v1/flow/agent/test`,
    pipeline: `${apiBase}/service/v1/flow/pipeline/test`
  },
  // 工作流API
  workflow: {
    nodeCapabilities: `${apiBase}/admin/flow/node-capabilities`,
    scriptLanguages: `${apiBase}/admin/flow/script-languages`,
    limits: `${apiBase}/admin/flow/limits`
  }
};

export default serviceApi;
