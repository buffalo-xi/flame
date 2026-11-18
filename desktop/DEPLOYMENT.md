# 部署说明

## 构建完成

large工程已成功构建为生产版本�?

### 构建输出目录：`dist/`

### 构建文件�?
- `dist/index.html` - 主HTML文件 (0.46 kB)
- `dist/assets/index-DAhXaZjZ.js` - JavaScript文件 (64.31 kB, gzip: 25.74 kB)
- `dist/assets/index-TUSp1xH4.css` - CSS文件 (2.21 kB, gzip: 0.72 kB)

### 构建信息�?
- 构建时间�?27ms
- 总大小（gzip）：26.77 kB
- Vite版本：v6.4.1

## 部署方法

### 方法1：静态文件服务器部署

#### 使用Python快速预览：
```bash
# Python 3
cd dist
python -m http.server 8080

# Python 2
cd dist
python -m SimpleHTTPServer 8080
```

#### 使用Node.js http-server�?
```bash
# 安装http-server
npm install -g http-server

# 启动服务�?
cd dist
http-server -p 8080
```

#### 使用Nginx部署�?
```nginx
server {
    listen 80;
    server_name your-domain.com;
    root /path/to/dist;
    index index.html;

    location / {
        try_files $uri $uri/ /index.html;
    }

    location ~* \.(js|css|png|jpg|jpeg|gif|ico|svg)$ {
        expires 1y;
        add_header Cache-Control "public, immutable";
    }
}
```

### 方法2：部署到CDN

将`dist/`目录下的所有文件上传到CDN�?
- 上传到阿里云OSS、腾讯云COS、AWS S3�?
- 配置CDN加�?
- 设置缓存策略

### 方法3：部署到云平�?

#### Vercel�?
```bash
# 安装Vercel CLI
npm i -g vercel

# 部署
vercel --prod
```

#### Netlify�?
```bash
# 安装Netlify CLI
npm i -g netlify-cli

# 部署
netlify deploy --prod --dir=dist
```

#### GitHub Pages�?
1. 将项目推送到GitHub
2. 在仓库设置中启用GitHub Pages
3. 选择`dist`目录作为发布�?

### 方法4：Docker部署

创建`Dockerfile`�?
```dockerfile
FROM nginx:alpine
COPY dist /usr/share/nginx/html
COPY nginx.conf /etc/nginx/conf.d/default.conf
EXPOSE 80
CMD ["nginx", "-g", "daemon off;"]
```

构建和运行：
```bash
docker build -t large-frontend .
docker run -p 80:80 large-frontend
```

## 部署检查清�?

- [ ] 构建文件完整性检�?
- [ ] 静态资源路径正确�?
- [ ] 环境变量配置（如需要）
- [ ] HTTPS证书配置
- [ ] CDN缓存策略设置
- [ ] 性能优化（gzip、压缩）
- [ ] SEO优化（meta标签、sitemap�?
- [ ] 监控和日志配�?

## 性能优化建议

1. **启用Gzip压缩**：已启用，文件大小减少约60%
2. **CDN加�?*：建议使用CDN加速静态资�?
3. **缓存策略**：设置合理的缓存时间
4. **图片优化**：使用WebP格式，适当压缩
5. **代码分割**：Vite已自动进行代码分�?

## 常见问题

### 1. 白屏问题
- 检查控制台错误
- 确认静态资源路径正�?
- 检查服务器配置

### 2. 路由问题
- 配置服务器支持SPA路由
- 使用`try_files`指令

### 3. 跨域问题
- 配置CORS策略
- 使用代理服务�?

## 生产环境配置

如需配置生产环境变量，创建`.env.production`文件�?

```env
VITE_API_BASE_URL=https://api.example.com
VITE_APP_TITLE=Large Frontend
```

重新构建�?
```bash
npm run build
```

## 监控和分�?

建议集成以下工具�?
- Google Analytics - 用户行为分析
- Sentry - 错误监控
- Lighthouse - 性能分析
- Web Vitals - 核心指标监控
