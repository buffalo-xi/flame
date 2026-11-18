// 导入API基础路径
import { apiBase } from './base.js';

// 导入各个模块的API配置
// import sysApi from './sys.js';
// import qcApi from './qc.js';
import adminApi from './admin.js';
import serviceApi from './service.js';

// 构建完整的API配置
const apiConfig = {
  // API基础路径
  baseUrl: apiBase,

  // 登录API
  login: `${apiBase}/admin/login`,

  // // 系统模块API
  // sys: sysApi,

  // // 质检模块API
  // qc: qcApi,

  // 管理模块API
  admin: adminApi,
  
  // 服务模块API
  service: serviceApi,
};

export default apiConfig;
