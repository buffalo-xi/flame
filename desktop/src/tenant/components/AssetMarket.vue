<template>
  <div class="asset-market">
    <!-- 头部：主 tab（市场 / 我的资产 / 安装记录）+ 右侧刷新与上架 -->
    <div class="am-header">
      <div class="am-tabs">
        <button class="am-tab" :class="{ active: tab === 'market' }" @click="switchTab('market')">
          <el-icon><Shop /></el-icon>
          本地市场
          <span class="am-count">
            ({{ market.length < marketTotal ? `${market.length}/${marketTotal}` : marketTotal }})
          </span>
        </button>
        <button class="am-tab" :class="{ active: tab === 'remote' }" @click="switchTab('remote')">
          <el-icon><Connection /></el-icon>
          远端市场
          <span class="am-count">({{ remoteCount }})</span>
        </button>
        <button class="am-tab" :class="{ active: tab === 'mine' }" @click="switchTab('mine')">
          <el-icon><Upload /></el-icon>
          我的资产
          <span class="am-count">({{ mine.length }})</span>
        </button>
        <button class="am-tab" :class="{ active: tab === 'installs' }" @click="switchTab('installs')">
          <el-icon><Download /></el-icon>
          安装记录
          <span class="am-count">({{ installs.length }})</span>
        </button>
      </div>
      <div class="am-header-actions">
        <el-button size="small" :loading="loading" @click="reload">
          <el-icon><Refresh /></el-icon>
          刷新
        </el-button>
        <el-button size="small" type="primary" @click="openListing">
          <el-icon><Plus /></el-icon>
          上架资产
        </el-button>
      </div>
    </div>

    <div class="am-body">
      <!-- ===== 本地市场（安装） ===== -->
      <div v-show="tab === 'market'" class="am-pane">
        <div class="am-filters">
          <button
            v-for="f in kindFilters"
            :key="f.value"
            class="am-chip"
            :class="{ active: kind === f.value }"
            @click="pickKind(f.value)"
          >{{ f.label }}</button>
          <!-- 来源：接入多个远端市场后，市里混着本机上架和几个站点导入的东西，
               没有这一层就没法「只看 test 站的资产」 -->
          <el-select
            v-model="origin"
            class="am-origin"
            size="small"
            clearable
            placeholder="全部来源"
            @change="loadMarket()"
          >
            <el-option
              v-for="o in origins"
              :key="o.server"
              :label="`${o.label}（${o.count}）`"
              :value="o.server || 'local'"
            />
          </el-select>
          <el-input
            v-model="keyword"
            class="am-search"
            size="small"
            clearable
            placeholder="搜索名称 / 描述，回车搜索"
            @keyup.enter="loadMarket()"
            @clear="loadMarket()"
          >
            <template #prefix><el-icon><Search /></el-icon></template>
          </el-input>
        </div>

        <div v-if="loading && !market.length" class="am-hint">加载中…</div>
        <div v-else-if="loadError" class="am-hint am-hint--error">{{ loadError }}</div>
        <el-empty
          v-else-if="!market.length"
          description="没有可安装的资产：要么已经装过了，要么提供方还没通过平台审核"
        />
        <!-- 短卡片网格：一行多张、随容器宽度自动换列，不要横向铺满整行的长条 -->
        <div v-else class="am-cards">
          <div v-for="a in market" :key="a.id" class="am-card">
            <div class="am-card-title">
              <span class="am-kind" :class="'am-kind--' + a.kind">{{ kindLabel(a.kind) }}</span>
              <span class="am-name" :title="a.display_name || a.name">{{ a.display_name || a.name }}</span>
              <span class="am-ver">v{{ a.build }}</span>
            </div>
            <div v-if="a.installed || a.upgradable" class="am-badges">
              <el-tag v-if="a.installed" size="small" type="success" effect="plain">已安装</el-tag>
              <el-tag v-if="a.upgradable" size="small" type="warning" effect="plain">有新的快照</el-tag>
            </div>
            <div class="am-desc">{{ a.description || '（提供方没有填描述）' }}</div>
            <div class="am-meta">
              <span>{{ a.file_count }} 个文件 · {{ fmtSize(a.total_size) }}</span>
              <span v-if="depsSummary(a.deps)" class="am-meta-deps">{{ depsSummary(a.deps) }}</span>
            </div>
            <div class="am-meta">{{ listerLabel(a) }}</div>
            <div class="am-card-actions">
              <el-button size="small" @click="openDetail(a)">详情</el-button>
              <el-button size="small" type="primary" @click="openInstall(a)">安装</el-button>
            </div>
          </div>
        </div>
        <!-- 分页：只给「加载更多」，不做页码条——顺序是 updated_at，翻页找比搜索慢 -->
        <div v-if="hasMore" class="am-more">
          <el-button size="small" :loading="loadingMore" @click="loadMoreMarket">
            加载更多（已显示 {{ market.length }} / {{ marketTotal }}）
          </el-button>
        </div>
      </div>

      <!-- ===== 远端市场（pull-through） ===== -->
      <!-- 与「本地市场」的区别：这里的东西**还没进本机**。平台只配了「这个租户能看哪个站」，
           不替租户逐条导入；点「拉取并安装」那一刻才下载到本机，并按对方的 install_policy
           决定是直接可用，还是先进本机待审（等平台审核通过）。 -->
      <div v-show="tab === 'remote'" class="am-pane">
        <div class="am-tip">
          这些资产<strong>还没进本机市场</strong>：平台只开通了「你能看哪个站」，不替你导入。
          点「拉取并安装」才从对方站下载成本机的平台资产；对方策略是「需审核」时，
          拉下来先进本机待审，平台通过后才真正装进来。
        </div>
        <div class="am-filters">
          <button
            v-for="f in kindFilters"
            :key="'r-' + f.value"
            class="am-chip"
            :class="{ active: kind === f.value }"
            @click="pickKind(f.value)"
          >{{ f.label }}</button>
          <!-- 站点：默认「全部」（各站并发拉）；某站总超时才切单站 -->
          <el-select
            v-model="remotePeer"
            class="am-origin"
            size="small"
            clearable
            placeholder="全部站点"
            @change="loadRemote()"
          >
            <!-- 下拉项里带站点地址，选中后仍只回显站名（:label 只用于回显与过滤）。
                 这样「几个站名字很接近」或「连不通要确认连的是谁」时不用逐个展开看 -->
            <el-option
              v-for="o in remotePeerOptions"
              :key="o.peer"
              :label="o.peer"
              :value="o.peer"
            >
              <span class="opt-peer">{{ o.peer }}</span>
              <span v-if="o.url" class="opt-peer-url" :title="o.url">{{ o.url }}</span>
            </el-option>
          </el-select>
          <!-- 远端不做分页：每站最多 500 条（在截断之前先按关键字过滤），
               所以「条目多」这件事靠搜索收敛，而不是翻页 -->
          <el-input
            v-model="remoteKeyword"
            class="am-search"
            size="small"
            clearable
            placeholder="搜索名称 / 描述，回车搜索"
            @keyup.enter="loadRemote()"
            @clear="loadRemote()"
          >
            <template #prefix><el-icon><Search /></el-icon></template>
          </el-input>
        </div>

        <div v-if="remoteLoading && !remotePeers.length" class="am-hint">加载中…</div>
        <div v-else-if="remoteError" class="am-hint am-hint--error">{{ remoteError }}</div>
        <el-empty
          v-else-if="!remotePeers.length"
          description="平台还没给本租户开通远端市场（或对端暂时不可达）"
        />
        <div v-else class="am-remote-blocks">
          <section v-for="p in remotePeers" :key="p.peer" class="am-remote-block">
            <!-- 标题行整行可点：一个站可能几十条，收起来才好「先看有哪些站」 -->
            <div class="am-remote-head" @click="togglePeerCollapse(p.peer)">
              <el-icon class="am-remote-toggle" :class="{ open: !collapsedPeers[p.peer] }">
                <ArrowRight />
              </el-icon>
              <span class="am-remote-name">{{ p.peer }}</span>
              <!-- 站点地址：同名站、连不通排查时要能看出「到底在连谁」。
                   纯展示 —— 拉取仍然只传 peer 名，不带 URL（无 SSRF 面） -->
              <span v-if="p.url" class="am-remote-url" :title="p.url">{{ p.url }}</span>
              <el-tag
                size="small"
                :type="p.install_policy === 'auto' ? 'warning' : 'info'"
                effect="plain"
              >{{ p.install_policy === 'auto' ? '拉取后直接可用' : '拉取后需平台审核' }}</el-tag>
              <span class="am-cell-sub">
                {{ p.count }} 个资产<template v-if="p.truncated">（只显示前 {{ p.items.length }} 条，用搜索收窄）</template>
              </span>
              <span class="am-cell-sub am-remote-fold">
                {{ collapsedPeers[p.peer] ? '展开' : '收起' }}
              </span>
            </div>
            <!-- 错误不跟着折叠：站挂了还要能一眼看到原因 -->
            <div v-if="p.error" class="am-hint am-hint--error">{{ p.error }}</div>
            <div v-else-if="collapsedPeers[p.peer]" class="am-cell-sub am-remote-folded">
              已收起 {{ p.items.length }} 条
            </div>
            <div v-else-if="!p.items.length" class="am-cell-sub">该站没有可拉取的资产</div>
            <div v-else class="am-cards">
              <div v-for="a in p.items" :key="p.peer + ':' + a.id" class="am-card">
                <div class="am-card-title">
                  <span class="am-kind" :class="'am-kind--' + a.kind">{{ kindLabel(a.kind) }}</span>
                  <span class="am-name" :title="a.display_name || a.name">{{ a.display_name || a.name }}</span>
                  <span class="am-ver">v{{ a.build }}</span>
                </div>
                <div v-if="a.local || a.installed" class="am-badges">
                  <el-tag v-if="a.local" size="small" type="info" effect="plain">
                    已拉取 v{{ a.local.build }}
                  </el-tag>
                  <el-tag v-if="a.local && a.local.status === 'pending'" size="small" type="warning" effect="plain">
                    等平台审核
                  </el-tag>
                  <el-tag v-if="a.installed" size="small" type="success" effect="plain">
                    已安装<template v-if="a.installed_build"> v{{ a.installed_build }}</template>
                  </el-tag>
                  <el-tag v-if="a.stale" size="small" type="warning" effect="plain">远端有新版</el-tag>
                </div>
                <div class="am-desc">{{ a.description || '（对方没有填描述）' }}</div>
                <div class="am-meta">
                  <span>{{ a.file_count }} 个文件 · {{ fmtSize(a.total_size) }}</span>
                  <span v-if="depsSummary(a.deps)" class="am-meta-deps">{{ depsSummary(a.deps) }}</span>
                </div>
                <div class="am-meta">来源：{{ p.peer }} 站</div>
                <div class="am-card-actions am-card-actions--show">
                  <el-button
                    size="small"
                    type="primary"
                    :loading="pullingKey === p.peer + ':' + a.id"
                    @click="pullAndInstall(p, a)"
                  >{{ pullActionLabel(a) }}</el-button>
                </div>
              </div>
            </div>
          </section>
        </div>
      </div>

      <!-- ===== 我的资产 ===== -->
      <div v-show="tab === 'mine'" class="am-pane">
        <div class="am-tip">
          上架的是一份<strong>快照</strong>：其它租户可以安装，装完就是他们自己的实体，
          与你的后续修改无关。再次上架同名资产，版本 +1（平台要求审核的租户会重新进入待审）。
        </div>
        <div class="am-toolbar">
          <el-button
            size="small"
            type="danger"
            plain
            :disabled="!mine.some((m) => m.status !== 'listed')"
            @click="clearMine"
          >清空未上架的资产</el-button>
        </div>
        <el-table v-loading="loading" :data="mine" size="small" style="width: 100%">
          <el-table-column label="名称" min-width="180">
            <template #default="{ row }">
              <div class="am-cell-name">{{ row.display_name || row.name }}</div>
              <div class="am-cell-sub">{{ row.name }}</div>
            </template>
          </el-table-column>
          <el-table-column label="类型" width="110">
            <template #default="{ row }">
              <span class="am-kind" :class="'am-kind--' + row.kind">{{ kindLabel(row.kind) }}</span>
            </template>
          </el-table-column>
          <el-table-column label="状态" width="120">
            <template #default="{ row }">
              <el-tooltip v-if="row.review_note" :content="row.review_note" placement="top">
                <el-tag size="small" :type="statusTagType(row.status)" effect="plain">{{ statusLabel(row.status) }}</el-tag>
              </el-tooltip>
              <el-tag v-else size="small" :type="statusTagType(row.status)" effect="plain">{{ statusLabel(row.status) }}</el-tag>
            </template>
          </el-table-column>
          <el-table-column label="快照" width="70">
            <template #default="{ row }">v{{ row.build }}</template>
          </el-table-column>
          <el-table-column label="可见范围" width="100">
            <template #default="{ row }">{{ row.visibility === 'allowlist' ? '部分租户' : '全部租户' }}</template>
          </el-table-column>
          <el-table-column label="更新时间" width="160">
            <template #default="{ row }">{{ fmtTime(row.updated_at) }}</template>
          </el-table-column>
          <el-table-column label="操作" width="140" fixed="right">
            <template #default="{ row }">
              <el-button
                v-if="row.status === 'pending'"
                link
                type="primary"
                size="small"
                @click="withdraw(row)"
              >撤回</el-button>
              <!-- 已通过的不能自己删：可能已经被别的租户装走，先让平台下架 -->
              <el-button
                v-if="row.status !== 'listed'"
                link
                type="danger"
                size="small"
                @click="removeMine(row)"
              >删除</el-button>
              <span v-else class="am-cell-sub">平台下架</span>
            </template>
          </el-table-column>
        </el-table>
      </div>

      <!-- ===== 安装记录 ===== -->
      <div v-show="tab === 'installs'" class="am-pane">
        <div class="am-tip">
          安装是<strong>复制</strong>：装完之后目标实体与资产快照无关，源租户改内容不会同步过来；
          「有新的快照」只表示提供方又上架过一次，需要重新安装并自行合并。
        </div>
        <div class="am-toolbar">
          <el-button
            size="small"
            type="danger"
            plain
            :disabled="!installs.length"
            @click="clearInstalls"
          >清空安装记录</el-button>
        </div>
        <el-table v-loading="loading" :data="installs" size="small" style="width: 100%">
          <el-table-column label="资产" min-width="160">
            <template #default="{ row }">
              <div class="am-cell-name">{{ row.asset_name }}</div>
              <div class="am-cell-sub">来源：{{ row.source_schema }}</div>
            </template>
          </el-table-column>
          <el-table-column label="类型" width="110">
            <template #default="{ row }">
              <span class="am-kind" :class="'am-kind--' + row.kind">{{ kindLabel(row.kind) }}</span>
            </template>
          </el-table-column>
          <el-table-column label="装成" min-width="140" prop="installed_name" />
          <el-table-column label="快照" width="120">
            <template #default="{ row }">
              v{{ row.build }}
              <el-tag v-if="row.upgradable" size="small" type="warning" effect="plain">最新 v{{ row.latest_build }}</el-tag>
            </template>
          </el-table-column>
          <el-table-column label="安装人" width="130">
            <template #default="{ row }">{{ row.installed_by_name || row.installed_by || '—' }}</template>
          </el-table-column>
          <el-table-column label="安装时间" width="160">
            <template #default="{ row }">{{ fmtTime(row.installed_at) }}</template>
          </el-table-column>
          <el-table-column label="操作" width="90" fixed="right">
            <template #default="{ row }">
              <el-button size="small" link type="danger" @click="removeInstall(row)">删除</el-button>
            </template>
          </el-table-column>
        </el-table>
      </div>
    </div>

    <!-- ===== 详情 ===== -->
    <el-dialog v-model="detailVisible" :title="detail?.display_name || detail?.name || '资产详情'" width="620px">
      <div v-if="detail" class="am-detail">
        <div class="am-detail-row">
          <span class="am-detail-key">类型</span>
          <span class="am-kind" :class="'am-kind--' + detail.kind">{{ kindLabel(detail.kind) }}</span>
          <span class="am-ver">v{{ detail.build }}</span>
        </div>
        <div class="am-detail-row">
          <span class="am-detail-key">{{ detail.listed_by?.origin?.server ? '来源' : '提供方' }}</span>
          <span>
            <template v-if="detail.listed_by?.origin?.server">
              从 {{ detail.listed_by.origin.server }} 站导入
              <span v-if="detail.listed_by.origin.imported_at" class="am-detail-sub">
                · {{ fmtTime(detail.listed_by.origin.imported_at) }}
              </span>
            </template>
            <template v-else>{{ listerLabel(detail) }}</template>
          </span>
        </div>
        <div class="am-detail-row">
          <span class="am-detail-key">内容</span>
          <span>{{ detail.file_count }} 个文件 · {{ fmtSize(detail.total_size) }}<template v-if="detail.entry"> · 入口 {{ detail.entry }}</template></span>
        </div>
        <div v-if="detail.description" class="am-detail-row">
          <span class="am-detail-key">说明</span>
          <span class="am-detail-desc">{{ detail.description }}</span>
        </div>
        <div class="am-detail-row am-detail-row--top">
          <span class="am-detail-key">依赖</span>
          <div class="am-detail-block">
            <div v-if="!detail.deps || !hasDeps(detail.deps)" class="am-cell-sub">无声明依赖</div>
            <template v-else>
              <div v-if="detail.deps.skills?.length">技能（按名字匹配目标租户）：
                <el-tag v-for="s in detail.deps.skills" :key="s" size="small" class="am-dep-tag">{{ s }}</el-tag>
              </div>
              <div v-if="detail.deps.private_skills?.length" class="am-cell-sub">
                私有技能 {{ detail.deps.private_skills.join('、') }} 不随资产迁移（当前版本只分发本体）
              </div>
              <div v-if="detail.deps.tools?.length">工具授权：
                <el-tag v-for="t in detail.deps.tools" :key="t.type + t.ref" size="small" class="am-dep-tag">
                  {{ t.ref }}<span v-if="t.grant_kind"> · {{ t.grant_kind }}</span>
                </el-tag>
              </div>
              <div v-if="detail.deps.requires?.length">能力要求：
                <el-tag v-for="r in detail.deps.requires" :key="r" size="small" type="warning" class="am-dep-tag">{{ r }}</el-tag>
              </div>
              <div v-if="detail.deps.egress">需要出网（net.egress）</div>
            </template>
          </div>
        </div>
        <div v-if="detail.review_note" class="am-detail-row am-detail-row--top">
          <span class="am-detail-key">审核意见</span>
          <span class="am-detail-desc">{{ detail.review_note }}</span>
        </div>
      </div>
      <template #footer>
        <el-button @click="detailVisible = false">关闭</el-button>
        <el-button v-if="detail" type="primary" @click="installFromDetail">安装</el-button>
      </template>
    </el-dialog>

    <!-- ===== 安装 ===== -->
    <el-dialog v-model="installVisible" :title="'安装：' + (installing?.display_name || installing?.name || '')" width="520px">
      <div v-if="installing && !installDone" class="am-form">
        <div class="am-form-item">
          <label>安装名</label>
          <el-input v-model="installForm.name" size="small" placeholder="留空则沿用资产名" />
          <div class="am-form-hint">
            装成一个<strong>新的</strong>实体；重名会提示建议名（不会静默改名）。
          </div>
        </div>
        <!-- 预检：与「导入」共用同一个渲染组件（同名提示 / 分组依赖 / 随包自带 / notes） -->
        <div v-if="installInspectLoading" class="am-form-hint">正在检查同名实体与依赖…</div>
        <AssetDepCheck v-else-if="installInspect" :inspect="installInspect"
                       :kind-label="kindLabelOf(installing.kind)" verb="安装" />

        <!-- 只有自定义工具有「停用」这一态（工作流只有正常 / 废弃，装完是正常） -->
        <div v-if="installing.kind === 'custom_tool'" class="am-form-item">
          <label>安装后状态</label>
          <el-radio-group v-model="installForm.status" size="small">
            <el-radio :value="1">启用（默认）</el-radio>
            <el-radio :value="2">停用（先复核脚本与配置）</el-radio>
          </el-radio-group>
          <div class="am-form-hint">停用的工具不会被调用，也不能发布版本；复核完再启用即可。</div>
        </div>

        <div class="am-form-item">
          <el-checkbox v-model="installForm.publish">安装成功后立刻发布一个版本</el-checkbox>
          <div class="am-form-hint">
            默认不发：装进来的是草稿，正式入口用的是已发布版本。停用或废弃的实体发不出去，会写进安装提示。
          </div>
        </div>
        <div v-if="installConflict" class="am-warn">{{ installConflict }}</div>
        <!-- 失败一律内联，不用 toast：改名 / 前置条件不满足时要能看着提示原地重试 -->
        <el-alert v-if="installError" :title="installError" type="error" :closable="false" show-icon />
        <div v-if="installing.kind === 'agent'" class="am-form-hint">
          智能体的超级权限、工具不限、技能发现等开关不会随资产迁移（装上均为关闭）；
          「谁能调用该智能体」需要在装好后到「智能体 → 调用授权」重新配置。
        </div>
        <div v-if="installing.kind === 'workflow'" class="am-form-hint">
          工作流只带图与变量，不带模型 / 工具 / 子流程的引用：装好后要在编辑器里逐项重配，
          凭据类环境变量（api_key 等）已在上架时清空。
        </div>
      </div>

      <!-- 结果页与「导入」共用同一个组件：成功行 + 注意事项 + 跳过文件，常驻到用户点完成 -->
      <AssetInstallResult v-if="installDone" :name="installDone.name" :overwritten="installDone.overwritten"
                          :warnings="installDone.warnings" :skipped="installDone.skipped"
                          :published="installDone.published" verb="安装" />

      <template #footer>
        <template v-if="installDone">
          <el-button type="primary" @click="installVisible = false">知道了</el-button>
        </template>
        <template v-else>
          <el-button @click="installVisible = false">取消</el-button>
          <el-button type="primary" :loading="submitting" @click="submitInstall">安装</el-button>
        </template>
      </template>
    </el-dialog>

    <!-- ===== 上架 ===== -->
    <el-dialog v-model="listingVisible" title="上架新资产" width="560px">
      <div class="am-form">
        <div class="am-form-item">
          <label>类型</label>
          <el-radio-group v-model="listingForm.kind" size="small" @change="onExposeKindChange">
            <el-radio value="skill">技能</el-radio>
            <el-radio value="custom_tool">自定义工具</el-radio>
            <el-radio value="agent">智能体</el-radio>
            <el-radio value="workflow">工作流</el-radio>
          </el-radio-group>
        </div>
        <div class="am-form-item">
          <label>选择对象</label>
          <el-select
            v-model="listingForm.ref"
            size="small"
            filterable
            :loading="entityLoading"
            placeholder="选择一个本租户的实体"
            style="width: 100%"
          >
            <el-option
              v-for="e in entityOptions"
              :key="e.ref"
              :label="e.label"
              :value="e.ref"
            >
              <span>{{ e.label }}</span>
              <span v-if="e.note" class="am-option-block">（{{ e.note }}）</span>
            </el-option>
          </el-select>
          <div class="am-form-hint">
            受保护实体（main / *-authoring 等）、超级权限智能体、废弃实体都能上架。
            私有技能<strong>不能单独上架</strong> —— 请上架它所属的智能体，私有技能会随智能体一起打包。
            打包出去的是一份<strong>内容副本</strong> —— 超权、工具不限、技能发现、
            调用授权、私有归属都不随包走；但<strong>依赖的自定义工具会连本体一起打包</strong>
            （默认启用）。
          </div>
        </div>
        <div class="am-form-hint">
          上架后<strong>默认即可见</strong>：本租户免审时其它租户立刻能看到并安装；
          平台对本租户要求审核时先进待审，通过后才可见。
        </div>
        <div v-if="listingWarnings.length" class="am-warn">
          <div v-for="(w, i) in listingWarnings" :key="i">{{ w }}</div>
        </div>
      </div>
      <template #footer>
        <el-button @click="listingVisible = false">关闭</el-button>
        <el-button type="primary" :loading="submitting" :disabled="!listingForm.ref" @click="submitListing">
          提交审核
        </el-button>
      </template>
    </el-dialog>
  </div>
</template>

<script setup>
import { ref, computed, onMounted, watch } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { Shop, Upload, Download, Refresh, Plus, Search, Connection, ArrowRight } from '@element-plus/icons-vue'
import adminApi from '../config/admin.js'
import { getToken } from '../utils/auth.js'
import { publishAssetVersion, OVERWRITE_COPY } from '../utils/assetTransfer.js'
import AssetDepCheck from './AssetDepCheck.vue'
import AssetInstallResult from './AssetInstallResult.vue'

const authHeaders = () => ({ Authorization: `Bearer ${getToken()}`, 'Content-Type': 'application/json' })

const tab = ref('market')
const loading = ref(false)
const loadError = ref('')
const submitting = ref(false)

// ===== 市场 =====
const market = ref([])
const kind = ref('')
const keyword = ref('')
// 来源筛选：'' = 全部；'local' = 本机上架；其它 = 对端站点名
const origin = ref('')
const origins = ref([])
const kindFilters = [
  { value: '', label: '全部' },
  { value: 'skill', label: '技能' },
  { value: 'custom_tool', label: '自定义工具' },
  { value: 'agent', label: '智能体' },
  { value: 'workflow', label: '工作流' }
]

const KIND_LABEL = { skill: '技能', custom_tool: '自定义工具', agent: '智能体', workflow: '工作流' }
const kindLabel = (k) => KIND_LABEL[k] || k || '—'

const STATUS_LABEL = {
  draft: '草稿',
  pending: '待审核',
  listed: '已上架',
  rejected: '已驳回',
  delisted: '已下架'
}
const statusLabel = (s) => STATUS_LABEL[s] || s || '—'
const statusTagType = (s) => {
  if (s === 'listed') return 'success'
  if (s === 'rejected') return 'danger'
  if (s === 'delisted') return 'info'
  if (s === 'pending') return 'warning'
  return 'info'
}

const fmtSize = (n) => {
  const v = Number(n) || 0
  if (v < 1024) return `${v} B`
  if (v < 1024 * 1024) return `${(v / 1024).toFixed(1)} KB`
  return `${(v / 1024 / 1024).toFixed(1)} MB`
}

const fmtTime = (t) => (t ? String(t).replace('T', ' ').slice(0, 19) : '—')

// 导入行的「提供方」在别的机器上：显式用 origin 判断，别靠 tenant.display_name 猜
// （后端为了不误导，已经把它换成了站点名，这里再说成「提供方」就更乱了）。
const listerLabel = (a) => {
  const src = a?.listed_by?.origin?.server
  if (src) return `来源：${src} 站`
  const tenant = a?.listed_by?.tenant?.display_name || a?.listed_by?.tenant?.schema || ''
  const user = a?.listed_by?.user?.display_name || ''
  if (user && tenant) return `提供方：${user} · ${tenant}`
  if (tenant) return `提供方：${tenant}`
  return ''
}

const hasDeps = (d) =>
  !!(d && ((d.skills && d.skills.length) || (d.private_skills && d.private_skills.length) ||
    (d.tools && d.tools.length) || (d.requires && d.requires.length) || d.egress))

const depsSummary = (d) => {
  if (!d) return ''
  const parts = []
  if (d.skills?.length) parts.push(`${d.skills.length} 个技能`)
  if (d.tools?.length) parts.push(`${d.tools.length} 项工具授权`)
  if (d.requires?.length) parts.push(`需要 ${d.requires.join('/')}`)
  if (d.egress) parts.push('需要出网')
  return parts.join(' · ')
}

// 本地市场分页：首屏 50 条，「加载更多」按 offset 追加。
// 不做页码：顺序按 updated_at，用户对「第 3 页」没有预期，页码只会把「找不到」变成「翻着找」。
const PAGE_SIZE = 50
const marketTotal = ref(0)
const hasMore = ref(false)
const loadingMore = ref(false)

const loadMarket = async (append = false) => {
  if (append) {
    loadingMore.value = true
  } else {
    loading.value = true
    loadError.value = ''
  }
  try {
    const offset = append ? market.value.length : 0
    const res = await fetch(
      adminApi.share.assets(kind.value, keyword.value.trim(), origin.value, PAGE_SIZE, offset),
      { headers: authHeaders() }
    )
    const body = await res.json().catch(() => ({}))
    if (body.code !== 200) throw new Error(body.msg || '读取资产列表失败')
    const items = body.data?.items || []
    market.value = append ? market.value.concat(items) : items
    marketTotal.value = body.data?.total ?? items.length
    hasMore.value = !!body.data?.has_more
    // 来源选项由后端汇总（忽略 origin 条件），筛了某个来源时其它选项不会消失
    if (!append) origins.value = body.data?.origins || []
  } catch (e) {
    if (append) ElMessage.error(e.message || '加载更多失败')
    else {
      loadError.value = e.message || '读取资产列表失败'
      market.value = []
      marketTotal.value = 0
      hasMore.value = false
    }
  } finally {
    loading.value = false
    loadingMore.value = false
  }
}

const loadMoreMarket = () => {
  if (hasMore.value && !loadingMore.value) loadMarket(true)
}

const pickKind = (v) => {
  kind.value = v
  // 两类列表各自带 kind 过滤：远端市场走服务端 ?kind=，本机市场走自己的接口
  if (tab.value === 'remote') loadRemote()
  else loadMarket()
}

// ===== 远端市场（pull-through） =====
// 平台只配「本租户能看哪个对端」，不替租户导入；这里列的是「本机还没有/还是旧版」的条目。
const remotePeers = ref([])
const remoteLoading = ref(false)
const remoteError = ref('')
const remoteLoaded = ref(false)
const pullingKey = ref('')
const remoteKeyword = ref('')
// 站点筛选：'' = 全部。默认一次全显示（各站并发拉，最慢那个不拖累别人），
// 某个站总是超时/条目太多时再切单站。选项来自「全部」那次的结果，切到单站后仍保留。
const remotePeer = ref('')
const remotePeerOptions = ref([])
// 站点分组折叠：{ [站点名]: true }。默认展开——一个站一个标题行，
// 只在配了多个站、或者某站条目特别多时才需要收起来。
const collapsedPeers = ref({})
const togglePeerCollapse = (name) => {
  collapsedPeers.value = { ...collapsedPeers.value, [name]: !collapsedPeers.value[name] }
}

// 计数用 count（匹配总数，可能是被截断前的），别用 items.length——
// 否则「200 条上限」会让 tab 上的数字比实际存在的少
const remoteCount = computed(() =>
  remotePeers.value.reduce((n, p) => n + (p.count ?? p.items?.length ?? 0), 0)
)

const loadRemote = async () => {
  remoteLoading.value = true
  remoteError.value = ''
  try {
    const peer = remotePeer.value || ''
    const res = await fetch(adminApi.share.remoteAssets(peer, kind.value, remoteKeyword.value.trim()), {
      headers: authHeaders()
    })
    const body = await res.json().catch(() => ({}))
    if (body.code !== 200) throw new Error(body.msg || '读取远端市场失败')
    // 单站不可达不影响其它站：error 是每个 peer 自己的字段，原样展示
    remotePeers.value = (body.data?.peers || []).filter((p) => p.items?.length || p.error)
    // 站点选项只从「全部」那次取：切到单站后返回的只有一站，拿它更新会把其它选项弄丢。
    // 连名带 url 一起存：下拉项要显示地址，而 v-model 的值仍必须是站名（后端按名过滤）
    if (!peer) {
      remotePeerOptions.value = remotePeers.value.map((p) => ({ peer: p.peer, url: p.url || '' }))
    }
    remoteLoaded.value = true
  } catch (e) {
    remoteError.value = e.message || '读取远端市场失败'
    remotePeers.value = []
  } finally {
    remoteLoading.value = false
  }
}

// 按钮文案：本机已有就直接装，等审核就只是提示，远端有新的快照则重拉
const pullActionLabel = (a) => {
  if (a.stale) return '拉取新版本'
  if (!a.local) return '拉取并安装'
  if (a.local.status === 'pending') return '等平台审核'
  return a.installed ? '重新安装' : '安装'
}

const pullAndInstall = async (p, a, forceRetry = false) => {
  // 本机已有且无需重拉：直接走安装弹窗（不必再下载一次）
  if (!forceRetry && a.local && a.local.status === 'listed' && !a.stale) {
    openInstall({
      id: a.local.asset_id,
      name: a.name,
      display_name: a.display_name,
      kind: a.kind,
      build: a.local.build,
      description: a.description,
      deps: a.deps,
      file_count: a.file_count,
      total_size: a.total_size
    })
    return
  }
  if (!forceRetry && a.local && a.local.status === 'pending' && !a.stale) {
    ElMessage.info('已拉取，正在等平台审核；通过后这一条会变成可安装')
    return
  }

  const key = `${p.peer}:${a.id}`
  pullingKey.value = key
  try {
    const payload = { peer: p.peer, asset_id: a.id }
    // 远端有新版 / 用户已确认覆盖：让后端跳过快路径，真正重新下载并覆盖
    if (forceRetry || a.stale) payload.force = true
    const res = await fetch(adminApi.share.pullAsset, {
      method: 'POST',
      headers: authHeaders(),
      body: JSON.stringify(payload)
    })
    const body = await res.json().catch(() => ({}))
    if (body.code !== 200) {
      // 同名但来源 id 变了（对方撤回后重新上架过）：确认一次再带 force 重试
      const cf = body.data?.conflict
      if (cf && !forceRetry) {
        try {
          await ElMessageBox.confirm(
            `本机已有同名资产「${cf.name}」v${cf.build}（${statusLabel(cf.status)}），` +
              '多半是对方撤回后重新上架过。覆盖后本机这一条会升到对方的最新快照，已安装它的租户需要重新安装。',
            '同名资产已存在',
            { type: 'warning', confirmButtonText: '覆盖并继续', cancelButtonText: '取消' }
          )
        } catch {
          return
        }
        return pullAndInstall(p, a, true)
      }
      throw new Error(body.msg || '拉取失败')
    }
    const data = body.data || {}
    await loadRemote()
    const item = {
      id: data.asset_id,
      name: data.name,
      display_name: data.display_name,
      kind: data.kind,
      build: data.build,
      description: data.description,
      deps: a.deps,
      file_count: a.file_count,
      total_size: a.total_size
    }
    if (data.listed) {
      // 对方策略是 auto：已经 listed，直接进安装流程
      openInstall(item)
    } else {
      ElMessage.success({ message: '已拉取，等待平台审核通过后即可安装', duration: 2400 })
    }
  } catch (e) {
    ElMessage.error(e.message || '拉取失败')
  } finally {
    pullingKey.value = ''
  }
}

// ===== 我的资产 / 安装记录 =====
const mine = ref([])
const installs = ref([])

const loadMine = async () => {
  const res = await fetch(adminApi.share.mine, { headers: authHeaders() })
  const body = await res.json().catch(() => ({}))
  if (body.code !== 200) throw new Error(body.msg || '读取我的资产失败')
  mine.value = body.data?.items || []
}

const loadInstalls = async () => {
  const res = await fetch(adminApi.share.installs, { headers: authHeaders() })
  const body = await res.json().catch(() => ({}))
  if (body.code !== 200) throw new Error(body.msg || '读取安装记录失败')
  installs.value = body.data?.items || []
}

// 一键清空：一次 DELETE，成功与否都要把市场和记录一起刷（「已安装」标记来自这张表）
const clearInstalls = async () => {
  try {
    await ElMessageBox.confirm(
      `清空本租户的全部 ${installs.value.length} 条安装记录？\n` +
        '只删记录，已安装的技能 / 工具 / 智能体都不会被卸载；' +
        '清空后这些资产在本地市场里会重新显示为「未安装」。',
      '清空安装记录',
      { type: 'warning', confirmButtonText: '全部清空', cancelButtonText: '取消' }
    )
  } catch {
    return
  }
  try {
    const res = await fetch(adminApi.share.clearInstalls, {
      method: 'DELETE',
      headers: authHeaders()
    })
    const body = await res.json().catch(() => ({}))
    if (body.code !== 200) throw new Error(body.msg || '清空失败')
    ElMessage.success(`已清空 ${body.data?.deleted ?? 0} 条安装记录`)
    await Promise.all([loadInstalls(), loadMarket()])
  } catch (e) {
    ElMessage.error(e.message || '清空失败')
  }
}

// 删安装记录：只抹掉这条流水，已装进租户的技能/工具/智能体都不动
const removeInstall = async (row) => {
  try {
    await ElMessageBox.confirm(
      `删除「${row.asset_name}」的那条安装记录？\n` +
        `只删记录，已安装的「${row.installed_name}」不会被卸载；` +
        '删掉后该资产在本地市场里会重新显示为「未安装」。',
      '删除安装记录',
      { type: 'warning', confirmButtonText: '删除记录', cancelButtonText: '取消' }
    )
  } catch {
    return
  }
  try {
    const res = await fetch(adminApi.share.deleteInstall(row.asset_id, row.installed_id), {
      method: 'DELETE',
      headers: authHeaders()
    })
    const body = await res.json().catch(() => ({}))
    if (body.code !== 200) throw new Error(body.msg || '删除失败')
    ElMessage.success('已删除安装记录')
    // 「已安装」标记来自这张表，市场列表要一起刷
    await Promise.all([loadInstalls(), loadMarket()])
  } catch (e) {
    ElMessage.error(e.message || '删除失败')
  }
}

// 三个 tab 的数据量都不大，一次全拉：否则切页要等、计数还得先加载
const reload = async () => {
  loading.value = true
  loadError.value = ''
  try {
    const tasks = [loadMarket(), loadMine(), loadInstalls()]
    if (remoteLoaded.value) tasks.push(loadRemote()) // 没打开过远端页签就不去打扰对端
    await Promise.all(tasks)
  } catch (e) {
    ElMessage.error(e.message || '加载失败')
  } finally {
    loading.value = false
  }
}

const switchTab = (t) => {
  tab.value = t
  // 远端市场要打对端 HTTP，首次进入才拉；之后靠「刷新」
  if (t === 'remote' && !remoteLoaded.value) loadRemote()
}

// ===== 详情 =====
const detailVisible = ref(false)
const detail = ref(null)

const openDetail = async (a) => {
  detail.value = a
  detailVisible.value = true
  try {
    const res = await fetch(adminApi.share.asset(a.id), { headers: authHeaders() })
    const body = await res.json().catch(() => ({}))
    if (body.code !== 200) throw new Error(body.msg || '读取详情失败')
    detail.value = body.data?.asset || a
  } catch (e) {
    ElMessage.warning(e.message || '详情加载失败，仅显示列表信息')
  }
}

// ===== 安装 =====
const installVisible = ref(false)
const installing = ref(null)
const installConflict = ref('')
const installError = ref('')
const installDone = ref(null)
const installForm = ref({ name: '', status: 1, publish: false })
const installInspect = ref(null)
const installInspectLoading = ref(false)
let inspectTimer = null
let inspectSeq = 0 // 名字连续改时只认最后一次回包
let inspectKey = '' // 上次真正查过的「asset_id + 名字」：打开弹窗时的显式查询与 watch 会用同一个键，避免重复请求

// 复用上面的 KIND_LABEL（市场列表与安装弹窗是同一套类型名）
const kindLabelOf = (k) => KIND_LABEL[k] || '实体'

// runInspect 与上传导入的预检同源：装之前就把同名实体与依赖摆出来，
// 而不是装完去 warnings 里数缺了几项
const runInspect = async () => {
  const a = installing.value
  if (!a) return
  // 键相同就不查：打开弹窗会显式查一次，name 变更的 watch 随后也会触发一次
  const key = a.id + '\u0000' + installForm.value.name.trim()
  if (key === inspectKey) return
  inspectKey = key

  const seq = ++inspectSeq
  installInspectLoading.value = true
  try {
    const res = await fetch(adminApi.share.inspectInstall, {
      method: 'POST',
      headers: authHeaders(),
      body: JSON.stringify({ asset_id: a.id, name: installForm.value.name.trim() })
    })
    const body = await res.json().catch(() => ({}))
    if (seq !== inspectSeq) return // 旧回包，丢掉
    installInspect.value = body.code === 200 ? body.data || null : { error: body.msg || '检查失败' }
  } catch (e) {
    if (seq === inspectSeq) installInspect.value = { error: e.message || '依赖检查失败' }
  } finally {
    if (seq === inspectSeq) installInspectLoading.value = false
  }
}

const scheduleInspect = () => {
  clearTimeout(inspectTimer)
  inspectTimer = setTimeout(runInspect, 300)
}

watch(() => installForm.value.name, scheduleInspect)

const openInstall = (a) => {
  installing.value = a
  installConflict.value = ''
  installError.value = ''
  installDone.value = null
  installInspect.value = null
  inspectKey = '' // 换了资产（或重开）：丢掉上次的键，强制重查
  installForm.value = {
    name: a.name || '',
    // 四类都默认启用（与后端缺省一致）；自定义工具想要先复核的，可以在这里选停用
    status: 1,
    publish: false
  }
  installVisible.value = true
  void runInspect()
}

const installFromDetail = () => {
  const a = detail.value
  detailVisible.value = false
  if (a) openInstall(a)
}

const postInstall = async (payload) => {
  const res = await fetch(adminApi.share.install, {
    method: 'POST',
    headers: authHeaders(),
    body: JSON.stringify(payload)
  })
  return res.json().catch(() => ({}))
}

const submitInstall = async () => {
  const a = installing.value
  if (!a) return
  submitting.value = true
  installConflict.value = ''
  installError.value = ''
  try {
    const payload = { asset_id: a.id }
    const nm = installForm.value.name.trim()
    if (nm && nm !== a.name) payload.name = nm
    if (a.kind === 'custom_tool') payload.status = installForm.value.status

    let body = await postInstall(payload)

    // 同名不是失败，而是「要你确认」：与上传导入同一套话术（OVERWRITE_COPY 共用）
    if (body.code !== 200 && body.data?.can_overwrite && OVERWRITE_COPY[a.kind]) {
      const copy = OVERWRITE_COPY[a.kind]
      try {
        await ElMessageBox.confirm(`${body.msg}。${copy.detail}`, copy.title, {
          type: 'warning', confirmButtonText: '覆盖', cancelButtonText: '取消'
        })
      } catch {
        return // 用户取消：当作没发生，不弹错误
      }
      body = await postInstall({ ...payload, overwrite: true })
    }

    if (body.code !== 200) {
      const suggested = body.data?.suggested_name
      if (suggested) {
        // 冲突不静默：把建议名填进去，由用户确认后再点一次安装
        installForm.value.name = suggested
        installConflict.value = `${body.msg || '名称已被占用'}；已填入建议名 ${suggested}，确认后再点一次「安装」`
        return
      }
      throw new Error(body.msg || '安装失败')
    }

    const data = body.data || {}
    const warnings = [...(data.warnings || [])]
    // 发布失败不回滚安装：两步是独立动作，失败只降级成提示
    let published = null
    if (installForm.value.publish) {
      const ref = a.kind === 'custom_tool' ? data.installed?.name : data.installed?.id
      try {
        published = await publishAssetVersion(a.kind, ref)
      } catch (e) {
        warnings.push(`安装已完成，但发布版本失败：${e.message}（可到版本面板手动发布）`)
      }
    }

    installDone.value = {
      name: data.installed?.name || payload.name || a.name,
      overwritten: !!data.overwritten,
      published,
      warnings,
      skipped: data.skipped_files || []
    }
    await Promise.all([loadMarket(), loadInstalls()])
  } catch (e) {
    // 与导入一致：失败留在弹窗里，看着提示原地重试
    installError.value = e.message || '安装失败'
  } finally {
    submitting.value = false
  }
}

// ===== 上架 =====
const listingVisible = ref(false)
const listingForm = ref({ kind: 'skill', ref: '' })
const listingWarnings = ref([])
const entityLoading = ref(false)
const entityOptions = ref([])

const openListing = () => {
  listingForm.value = { kind: 'skill', ref: '' }
  listingWarnings.value = []
  entityOptions.value = []
  listingVisible.value = true
  loadEntities('skill')
}

const onExposeKindChange = (v) => {
  listingForm.value.ref = ''
  entityOptions.value = []
  loadEntities(v)
}

// 各类型的实体清单按需拉：一次只拉当前类型，避免为了一行下拉去请求三张表
const loadEntities = async (k) => {
  entityLoading.value = true
  try {
    if (k === 'skill') {
      const res = await fetch(adminApi.skills.base, {
        method: 'POST',
        headers: authHeaders(),
        // 接口把 Limit<=0 归一成 20（仓储层的 -1=全部只有内部能传），这里给一个大值
        body: JSON.stringify({ page: 1, limit: 500 })
      })
      const body = await res.json().catch(() => ({}))
      if (body.code !== 200) throw new Error(body.msg || '读取技能失败')
      entityOptions.value = (body.data?.items || [])
        // 私有技能不能单独上架（后端会拒绝）：正文住在所属智能体的目录里，只能随智能体走，
        // 否则装到别的租户只剩一个丢了归属的租户级副本
        .filter((s) => !s.agent_id)
        .map((s) => ({
          ref: s.id,
          label: s.name,
          note: s.protected ? '系统内置'
            : Number(s.status) === 3 ? '已废弃' : ''
        }))
    } else if (k === 'custom_tool') {
      const res = await fetch(adminApi.customTools.list, { headers: authHeaders() })
      const body = await res.json().catch(() => ({}))
      if (body.code !== 200) throw new Error(body.msg || '读取自定义工具失败')
      entityOptions.value = (body.data?.tools || []).map((t) => ({
        ref: t.name,
        label: t.name,
        note: Number(t.status) === 3 ? '已废弃' : ''
      }))
    } else if (k === 'workflow') {
      // 工作流没有目录，只有 {schema}.flows 里的一行；上架时后端把它序列化成 FLOW.json
      const res = await fetch(adminApi.flows.base, {
        method: 'POST',
        headers: authHeaders(),
        body: JSON.stringify({ page: 1, limit: 500 })
      })
      const body = await res.json().catch(() => ({}))
      if (body.code !== 200) throw new Error(body.msg || '读取工作流失败')
      entityOptions.value = (body.data?.items || []).map((f) => ({
        ref: f.id,
        label: f.name,
        note: Number(f.status) === 3 ? '已废弃' : ''
      }))
    } else {
      const res = await fetch(adminApi.agents.base, {
        method: 'POST',
        headers: authHeaders(),
        body: JSON.stringify({ page: 1, limit: 500 })
      })
      const body = await res.json().catch(() => ({}))
      if (body.code !== 200) throw new Error(body.msg || '读取智能体失败')
      entityOptions.value = (body.data?.items || []).map((a) => ({
        ref: a.id,
        label: a.name,
        note: a.is_supervisor ? '超级权限（权限不随包走）'
          : a.protected ? '系统内置'
          : Number(a.status) === 3 ? '已废弃' : ''
      }))
    }
  } catch (e) {
    ElMessage.error(e.message || '读取可上架对象失败')
    entityOptions.value = []
  } finally {
    entityLoading.value = false
  }
}

const submitListing = async () => {
  if (!listingForm.value.ref) return
  submitting.value = true
  listingWarnings.value = []
  try {
    const res = await fetch(adminApi.share.listings, {
      method: 'POST',
      headers: authHeaders(),
      body: JSON.stringify(listingForm.value)
    })
    const body = await res.json().catch(() => ({}))
    if (body.code !== 200) throw new Error(body.msg || '上架失败')
    const warnings = body.data?.warnings || []
    listingWarnings.value = warnings
    // need_review=false 表示该租户免审、这条已经直接上架（缺省即如此），文案要跟着变
    ElMessage.success({
      message: body.data?.need_review === false ? '已上架，其它租户现在就能安装' : '已提交，等待平台审核',
      duration: 1800
    })
    tab.value = 'mine'
    await Promise.all([loadMine(), loadMarket()])
    if (!warnings.length) listingVisible.value = false
  } catch (e) {
    ElMessage.error(e.message || '上架失败')
  } finally {
    submitting.value = false
  }
}

// ===== 直接删除 / 清空 =====
// 与「撤回」的区别：撤回是流转（pending → draft），这里是直接清掉；
// 草稿 / 待审 / 被驳回 / 已下架的都能删，已上架的不行（可能已被别人装走）。
const removeMine = async (row) => {
  try {
    await ElMessageBox.confirm(
      `删除「${row.name}」（当前状态：${statusLabel(row.status)}）？` +
        '快照文件会一起清掉，删除后不可恢复。',
      '删除资产',
      { type: 'warning', confirmButtonText: '删除', cancelButtonText: '取消' }
    )
  } catch {
    return
  }
  try {
    const res = await fetch(adminApi.share.deleteMine(row.id), {
      method: 'DELETE',
      headers: authHeaders()
    })
    const body = await res.json().catch(() => ({}))
    if (body.code !== 200) throw new Error(body.msg || '删除失败')
    ElMessage.success('已删除')
    await Promise.all([loadMine(), loadMarket()])
  } catch (e) {
    ElMessage.error(e.message || '删除失败')
  }
}

const clearMine = async () => {
  const removable = mine.value.filter((m) => m.status !== 'listed').length
  try {
    await ElMessageBox.confirm(
      `清空本租户 ${removable} 条未上架的资产（草稿 / 待审 / 被驳回 / 已下架）？\n` +
        '快照文件会一起清掉，删除后不可恢复；已上架的资产会保留（需要先让平台下架）。',
      '清空未上架的资产',
      { type: 'warning', confirmButtonText: '全部清空', cancelButtonText: '取消' }
    )
  } catch {
    return
  }
  try {
    const res = await fetch(adminApi.share.clearMine, {
      method: 'DELETE',
      headers: authHeaders()
    })
    const body = await res.json().catch(() => ({}))
    if (body.code !== 200) throw new Error(body.msg || '清空失败')
    ElMessage.success(`已清空 ${body.data?.deleted ?? 0} 条资产`)
    await Promise.all([loadMine(), loadMarket()])
  } catch (e) {
    ElMessage.error(e.message || '清空失败')
  }
}

// ===== 撤回 =====
const withdraw = async (row) => {
  const isDraft = row.status === 'draft'
  try {
    await ElMessageBox.confirm(
      isDraft
        ? `删除草稿「${row.name}」？快照文件会一起清掉，删除后不可恢复。`
        : `撤回「${row.name}」？撤回后回到草稿状态，其它租户将看不到它。`,
      isDraft ? '删除草稿' : '撤回上架',
      { type: 'warning', confirmButtonText: isDraft ? '删除' : '撤回', cancelButtonText: '取消' }
    )
  } catch {
    return
  }
  try {
    const res = await fetch(adminApi.share.withdraw(row.id), { method: 'POST', headers: authHeaders() })
    const body = await res.json().catch(() => ({}))
    if (body.code !== 200) throw new Error(body.msg || '操作失败')
    ElMessage.success({ message: isDraft ? '已删除' : '已撤回', duration: 1500 })
    await Promise.all([loadMine(), loadMarket()])
  } catch (e) {
    ElMessage.error(e.message || '操作失败')
  }
}

onMounted(reload)
</script>

<style scoped>
.asset-market {
  position: relative;
  width: 100%;
  height: 100%;
  display: flex;
  flex-direction: column;
  padding: 20px;
  background: #fafafa;
  overflow: hidden;
}

/* 头部：与技能页 / 工具页的同款药丸 tab */
.am-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  margin-top: 2px;
  margin-bottom: 20px;
  flex-shrink: 0;
}

.am-tabs {
  display: flex;
  align-items: center;
  gap: 4px;
}

.am-tab {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  height: 38px;
  padding: 8px 18px;
  font-size: 14px;
  font-weight: 500;
  line-height: 1;
  white-space: nowrap;
  color: #666;
  background: none;
  border: 2px solid #fff;
  border-radius: 10px;
  cursor: pointer;
  transition: all 0.3s ease;
}

.am-tab:hover {
  background: #e6e6e6;
  color: #333;
}

.am-tab.active {
  background: #e0e0e0;
  color: #333;
}

.am-count {
  font-size: 12px;
  color: #909399;
}

.am-header-actions {
  display: flex;
  align-items: center;
  gap: 8px;
}

.am-body {
  flex: 1;
  min-height: 0;
  overflow: auto;
  /* 滚动条：位置常驻（一直 6px，不占 15px 的原生宽度），拇指默认透明 ——
     鼠标移入才上色。这样不进入时不显示，进入时内容也不会横向跳动 */
  scrollbar-width: thin;
  scrollbar-color: transparent transparent;
}

.am-body::-webkit-scrollbar {
  width: 6px;
  height: 6px;
}

.am-body::-webkit-scrollbar-track {
  background: transparent;
}

.am-body::-webkit-scrollbar-thumb {
  background: transparent;
  border-radius: 3px;
}

.am-body:hover {
  scrollbar-color: rgba(144, 147, 153, 0.4) transparent;
}

.am-body:hover::-webkit-scrollbar-thumb {
  background: rgba(144, 147, 153, 0.4);
}

.am-pane {
  display: flex;
  flex-direction: column;
  gap: 12px;
}

/* 市场筛选行 */
.am-filters {
  display: flex;
  align-items: center;
  gap: 4px;
  flex-wrap: wrap;
}

.am-chip {
  height: 28px;
  padding: 0 12px;
  font-size: 13px;
  color: #666;
  background: #fff;
  border: 1px solid #e4e7ed;
  border-radius: 14px;
  cursor: pointer;
  transition: all 0.2s;
}

.am-chip:hover {
  border-color: #1890ff;
  color: #1890ff;
}

.am-chip.active {
  background: #e6f0f8;
  border-color: #b3d4f0;
  color: #1890ff;
  font-weight: 500;
}

/* 来源下拉：放在 kind 药丸之后、搜索框之前；宽度固定，避免选项文案长短把筛选行挤变形 */
.am-origin {
  width: 168px;
  flex-shrink: 0;
}

/* 搜索框跟筛选行一起左对齐：不要 margin-left: auto 把它顶到最右边 */
.am-search {
  width: 240px;
  flex-shrink: 0;
}

.am-hint {
  padding: 24px;
  text-align: center;
  font-size: 13px;
  color: #909399;
}

.am-hint--error {
  color: #f56c6c;
}

/* 表格上方的操作条：按钮贴右 */
.am-toolbar {
  display: flex;
  justify-content: flex-end;
  align-items: center;
  margin: -4px 0 8px;
}

/* 加载更多：居中一行，不做页码条 */
.am-more {
  display: flex;
  justify-content: center;
  padding: 14px 0 4px;
}

/* 远端市场：按对端分组，每组一个标题行 + 一屏卡片 */
.am-remote-blocks {
  display: flex;
  flex-direction: column;
  gap: 18px;
}

.am-remote-block {
  display: flex;
  flex-direction: column;
  gap: 10px;
}

.am-remote-head {
  display: flex;
  align-items: center;
  gap: 8px;
  min-height: 44px;
  padding: 10px 4px;
  border-bottom: 1px solid #ebeef5;
  cursor: pointer;
  user-select: none;
}

.am-remote-head:hover .am-remote-name {
  color: #1890ff;
}

/* 折叠箭头：展开时朝下，收起时朝右 */
.am-remote-toggle {
  font-size: 12px;
  color: #909399;
  transition: transform 0.15s;
}

.am-remote-toggle.open {
  transform: rotate(90deg);
}

.am-remote-fold {
  margin-left: auto; /* 「展开/收起」贴右，不挤占资产数 */
}

.am-remote-folded {
  padding: 6px 2px;
}

.am-remote-name {
  font-size: 14px;
  font-weight: 600;
  color: #303133;
}

/* 站点地址：次要信息，跟在站名后面。给 max-width + 省略号，
   避免长域名把「资产数 / 展开收起」挤到换行；完整地址看 title */
.am-remote-url {
  font-size: 12px;
  color: #909399;
  max-width: 320px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  flex-shrink: 1;
  min-width: 0;
}

/* 站点下拉项：站名 + 地址。用 inline-block 而不是改 el-option 的 li 布局，
   免得跟 Element 自己的 flex/padding 打架；地址给个上限并省略，长域名不会把下拉撑到屏外 */
.opt-peer {
  display: inline-block;
  vertical-align: middle;
}

.opt-peer-url {
  display: inline-block;
  margin-left: 8px;
  font-size: 12px;
  color: #909399;
  max-width: 220px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  vertical-align: middle;
}

/* 远端卡片的动作是这一页的主操作（拉取），不要等 hover 才浮现 */
.am-card-actions--show {
  opacity: 1;
  transform: none;
  pointer-events: auto;
}

/* 短卡片网格：一行多张（220px 起，随宽度自适应列数），卡片自身竖直排布、
   高度由行内最高的一张统一拉伸，操作按钮永远贴底 */
.am-cards {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(220px, 1fr));
  gap: 10px;
  align-items: stretch;
}

.am-card {
  position: relative; /* 操作浮层的定位基准 */
  display: flex;
  flex-direction: column;
  gap: 6px;
  min-width: 0;
  padding: 12px;
  background: #fff;
  border: 1px solid #ebeef5;
  border-radius: 10px;
  transition: border-color 0.2s, box-shadow 0.2s;
}

.am-card:hover {
  border-color: #d6e4f0;
  box-shadow: 0 2px 10px rgba(24, 144, 255, 0.06);
}

/* 标题行：类型徽标 + 名称（长得放不下就省略号，悬停看全名）+ 版本 */
.am-card-title {
  display: flex;
  align-items: center;
  gap: 6px;
  min-width: 0;
}

.am-name {
  flex: 1;
  min-width: 0;
  font-size: 15px;
  font-weight: 600;
  color: #303133;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.am-badges {
  display: flex;
  align-items: center;
  gap: 6px;
  flex-wrap: wrap;
}

.am-ver {
  font-size: 12px;
  color: #909399;
}

.am-kind {
  display: inline-block;
  padding: 1px 8px;
  font-size: 12px;
  line-height: 18px;
  border-radius: 9px;
  background: #f0f2f5;
  color: #606266;
  white-space: nowrap;
}

.am-kind--skill {
  background: #eef6ff;
  color: #1874d0;
}

.am-kind--custom_tool {
  background: #f4f0ff;
  color: #6b47c9;
}

.am-kind--agent {
  background: #effaf3;
  color: #2c9b62;
}

.am-kind--workflow {
  background: #fff7e6;
  color: #c47b00;
}

/* 描述最多两行（固定两行高，同一行卡片的元信息才会对齐；超出省略，详情里看全文）。
   字号与智能体卡片的次要文字一致：12px / #909399 */
.am-desc {
  font-size: 12px;
  color: #909399;
  line-height: 1.5;
  min-height: calc(1.5em * 2);
  display: -webkit-box;
  -webkit-line-clamp: 2;
  -webkit-box-orient: vertical;
  overflow: hidden;
  word-break: break-word;
}

.am-meta {
  display: flex;
  gap: 10px;
  flex-wrap: wrap;
  font-size: 12px;
  line-height: 1.5;
  color: #909399;
  min-width: 0;
}

.am-meta-deps {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  max-width: 100%;
}

/* 操作浮层：绝对定位在卡片右下角，平时 opacity 0 + 不接指针事件 ——
   既不占卡片空间，也不会挡住卡片的点击；鼠标移入（或键盘聚焦）才浮现。
   白底 + 一点阴影，浮在提供方那行文字之上时仍然看得清。 */
.am-card-actions {
  position: absolute;
  right: 8px;
  bottom: 8px;
  display: flex;
  align-items: center;
  gap: 6px;
  padding: 4px;
  border-radius: 8px;
  background: rgba(255, 255, 255, 0.94);
  box-shadow: 0 2px 8px rgba(0, 0, 0, 0.1);
  opacity: 0;
  transform: translateY(2px);
  pointer-events: none;
  transition: opacity 0.18s ease, transform 0.18s ease;
}

.am-card:hover .am-card-actions,
.am-card:focus-within .am-card-actions {
  opacity: 1;
  transform: none;
  pointer-events: auto;
}

/* 表格与提示 */
.am-tip {
  padding: 10px 12px;
  font-size: 12px;
  line-height: 1.6;
  color: #7a6a3d;
  background: #fdf6ec;
  border: 1px solid #faecd8;
  border-radius: 8px;
}

.am-cell-name {
  font-size: 13px;
  color: #303133;
}

.am-cell-sub {
  font-size: 12px;
  color: #909399;
}

/* 详情 */
.am-detail {
  display: flex;
  flex-direction: column;
  gap: 10px;
}

.am-detail-row {
  display: flex;
  align-items: center;
  gap: 8px;
  font-size: 13px;
  color: #303133;
}

.am-detail-row--top {
  align-items: flex-start;
}

.am-detail-key {
  width: 64px;
  flex-shrink: 0;
  color: #909399;
}

.am-detail-desc {
  white-space: pre-wrap;
  word-break: break-word;
  line-height: 1.6;
}

.am-detail-sub {
  font-size: 12px;
  color: #909399;
}

.am-detail-block {
  display: flex;
  flex-direction: column;
  gap: 6px;
  line-height: 1.6;
}

.am-dep-tag {
  margin: 0 4px 2px 0;
}

/* 表单与结果 */
.am-form {
  display: flex;
  flex-direction: column;
  gap: 14px;
}

.am-form-item {
  display: flex;
  flex-direction: column;
  gap: 6px;
}

.am-form-item > label {
  font-size: 13px;
  font-weight: 500;
  color: #606266;
}

.am-form-hint {
  font-size: 12px;
  line-height: 1.6;
  color: #909399;
}

/* 预检 / 结果两块已抽成 AssetDepCheck / AssetInstallResult，样式随组件走 */

.am-option-block {
  margin-left: 6px;
  font-size: 12px;
  color: #c0c4cc;
}

.am-warn {
  padding: 10px 12px;
  font-size: 12px;
  line-height: 1.6;
  color: #b26a00;
  background: #fdf6ec;
  border: 1px solid #faecd8;
  border-radius: 8px;
}

</style>
