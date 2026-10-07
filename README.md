# Paca task check-in plugin

独立、开源的 Paca v0.18.6 拍照打卡插件。官方 Paca 保持原版；本插件通过官方 REST API 更新任务。

开发中。生产启用须先通过对应 Action、部署检查及用户真机验收；尚未通过的能力不会标为交付。

## 安全与构建

所有编译、lint、测试和镜像构建在 GitHub Actions。公共仓库／镜像不包含真实凭据。worker 使用自己的 schema 和受限 API key，照片存私有对象前缀，短期链接只授权单实例／卡次。默认各提前十分钟，截止严格，独立两卡，开始进行中／结束完成，照片保留九十天。

通用动作采用 PushGo metadata.action_version=1 / action_kind=web / action_url / action_label；队列插件独立，未启用打卡时保持普通提醒。

部分插件控制和 AES-GCM 辅助代码沿用 Self-Command/paca-plugin-tasknotes-webhook；其 crypto helpers 源于 Apache-2.0 Paca-AI/paca-plugin-webhook v0.2.0。保留 LICENSE 和来源说明。

## 独立安装和运行

1. 只下载同一成功 Release 的 plugin-install、checksums、image-digest 和验证报告。解压 wasm/frontend 到现有 Paca 插件挂载目录，通过管理 API 注册 plugin.json，启用后核对 /health 的源码 SHA 和 schema 1。
2. PostgreSQL 为 worker 创建仅可访问 `plugin_data_com_selfcommand_task_checkin` 的角色；默认权限、表和 sequence 权限均限该 schema。核心任务只通过官方 REST API 写入。插件迁移由官方宿主执行。
3. 使用仅加入目标项目的专用 Paca 服务账号，授予任务读写、状态读取和接入插件 source-link 读取权限，创建 API key。不要把生产管理员 key 用作常驻 worker key。
4. 通过插件管理凭据接口创建 worker secret，分别在运行时挂载 API key、worker secret、grant secret、internal action secret、S3 凭据文件。grant/action secret 分开、随机不少于 32 字节；不填入网页、镜像或插件 JSON。
5. 对象存储使用独立私有 bucket 和 `checkin/<project>/<instance>/` 前缀，仅为 worker 授权。部署 `deploy/compose.checkin.yaml` overlay，镜像引用锁定 digest，合并 `deploy/nginx-checkin.conf` 到现有 HTTPS server。内部接口只在受控容器网络访问。
6. 在项目设置绑定进行中、完成、归档的实际 UUID，再启用。任务详情可确认精确时间和覆盖提前分钟数。日期字段与精确时刻分别保存；时间不完整不安排窗口。

## 授权与同步接口

`/checkin-api/v1/` 面向短期单卡会话；fragment 换取 HttpOnly、Secure、SameSite 会话后立即从地址清除。浏览器不持有 Paca 或 PushGo 凭据。临时照片与成功照片均有持久清理状态，上传失败重启后可回收；规范化为 JPEG、移除 EXIF，限制 10 MiB 和 2500 万像素。

`/checkin-api/v1/sync/` 使用每设备可撤销配对令牌，读取范围限项目、来源连接和已记录媒体。增量记录、照片、状态版本分别处理。状态写回要求当前 revision；旧打卡记录仍可下载照片，但不能覆盖较新状态。冲突和重复解决保留原结果。

`/internal/v1/action`、`/internal/v1/task`、`/internal/v1/writeback-match` 使用独立内部授权，仅供 B/A 对接；插件之间不访问其他 schema。receipt 匹配要求来源、路径、状态、正文 SHA 和当前实例 revision 全部一致。实际防循环还需要 A 和 D 对接后完成全链路测试，本阶段不把接口存在等同于全链路已通过。

## 验证边界

Action 运行 Go vet/race 单测、TinyGo、TypeScript、生产网页构建、官方 Paca 注册/迁移/启停、私有 RustFS 上传、移动浏览器页面及数据库截止检查。截图和报告绑定源码 SHA。真实 Android 相机、HMS 后台通知及 Obsidian 回写属于后续集成验收，未执行时明确列为未验证。

数据库访问为每个插件加独立查询标识，避免官方宿主共享 PostgreSQL 连接池在切换 schema 后复用其他插件的缓存执行计划。保持独立 schema、角色和 API 边界，使用三个插件的匹配发行产物。
