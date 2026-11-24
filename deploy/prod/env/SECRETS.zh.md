# Flame 部署的密钥

[English](SECRETS.md) | 中文

所有密钥都在 `/etc/flame/flame.env`（0600，属主 root），由 `install.sh` 用 `openssl rand -hex 24` 逐个生成。
镜像里没有密钥，release 里的配置文件也没有：只有这一个文件。

用 hex 而不是 base64 不是风格问题：`POSTGRES_PASSWORD` 会被拼进 LiteLLM 的 `DATABASE_URL`，base64 的 `+`、`/`、
`=` 会让这个 URI 失效。

## 影响面与轮换

| 变量 | 保护什么 | 轮换的影响 |
|---|---|---|
| `FLAME_TAG` | 不是密钥；钉住所有镜像与 release 目录 | 它**就是**升级/回退机制 |
| `POSTGRES_PASSWORD` | `flame` 角色，以及两个库 | 先 `ALTER ROLE`，再改这里并重建 `nexus`、`migrate`、`litellm`。只改这里**不会**改已有角色的口令：initdb 只对空数据目录执行 |
| `REDIS_PASSWORD` | 网关粘性绑定、run ledger、配额计数 | 还要同时改 `redis.conf`（`flame` 包装脚本读这个 env，Redis 不读），然后重启 redis、nexus 与两个网关 |
| `JWT_SECRET` | 全部会话令牌 | 轮换会让所有会话立刻失效——可接受；多副本必须一致 |
| `FLAME_VALUE_KEY` | `kind=secret` 的 `global_vars` 行 | **轮换会让所有已存密文不可读。** 本版本没有双 key 过渡，按"只写一次"对待 |
| `LITELLM_MASTER_KEY` | nexus → LiteLLM 控制面 | 改这里并重启 `nexus`；网关从自己的环境读同一个值 |
| `LITELLM_SALT_KEY` | LiteLLM 库里的供应商 key | **永不轮换。** 所有已存供应商 key 都解不开，模型还在列表里但每次调用都是认证失败 |
| `SANDBOX_TOKEN` | nexus ↔ sandboxg ↔ sandboxd | 两端必须同时改；不一致的表现是每次调用 401，而不是启动报错 |
| `BROWSER_TOKEN` | nexus ↔ browserg ↔ browserd | 同 `SANDBOX_TOKEN` |
| `FLAME_ADMIN_PASSWORD` | bootstrap 作业创建的种子账号 | 首次登录后在控制台改；它只在账号**创建**时被读取 |
| `FLAME_API_KEY` | 租户主用户的 API key | 在控制台轮换或删除；种子作业只补缺失的那一条 |
| `CONSUL_HTTP_TOKEN`（在 `sandboxd.env`） | 启用 Consul ACL 时的注册与发现 | 注册端（执行器）与发现端（网关）都要 |

## 多副本必须一致的值

`JWT_SECRET` 与 `FLAME_VALUE_KEY` 属于整个部署而不是单机：两个副本用不同的值，用户会被随机登出，各自的密文
也互相解不开。`SANDBOX_TOKEN` / `BROWSER_TOKEN` 与数据库口令同理——网关和一次性作业也要用同一份。

## 备份

这些值只存在于 `/etc/flame`。加密备份它；丢了意味着要重签所有令牌、重填所有供应商 key，而不是"从数据库恢复"。
