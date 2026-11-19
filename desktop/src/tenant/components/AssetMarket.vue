<template>
  <div class="asset-market">
    <!-- 头部：主 tab（市场 / 我的资产 / 安装记录）+ 右侧刷新与上架 -->
    <div class="am-header">
      <div class="am-tabs">
        <button class="am-tab" :class="{ active: tab === 'market' }" @click="switchTab('market')">
          <el-icon><Shop /></el-icon>
          {{ t('assetMarket.tabMarket') }}
          <span class="am-count">
            ({{ market.length < marketTotal ? `${market.length}/${marketTotal}` : marketTotal }})
          </span>
        </button>
        <button class="am-tab" :class="{ active: tab === 'remote' }" @click="switchTab('remote')">
          <el-icon><Connection /></el-icon>
          {{ t('assetMarket.tabRemote') }}
          <span class="am-count">({{ remoteCount }})</span>
        </button>
        <button class="am-tab" :class="{ active: tab === 'mine' }" @click="switchTab('mine')">
          <el-icon><Upload /></el-icon>
          {{ t('assetMarket.tabMine') }}
          <span class="am-count">({{ mine.length }})</span>
        </button>
        <button class="am-tab" :class="{ active: tab === 'installs' }" @click="switchTab('installs')">
          <el-icon><Download /></el-icon>
          {{ t('assetMarket.tabInstalls') }}
          <span class="am-count">({{ installs.length }})</span>
        </button>
      </div>
      <div class="am-header-actions">
        <el-button size="small" :loading="loading" @click="reload">
          <el-icon><Refresh /></el-icon>
          {{ t('assetMarket.refresh') }}
        </el-button>
        <el-button size="small" type="primary" @click="openListing">
          <el-icon><Plus /></el-icon>
          {{ t('assetMarket.listNew') }}
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
            :placeholder="t('assetMarket.originAll')"
            @change="loadMarket()"
          >
            <el-option
              v-for="o in origins"
              :key="o.server"
              :label="t('assetMarket.originOption', { label: o.label, count: o.count })"
              :value="o.server || 'local'"
            />
          </el-select>
          <el-input
            v-model="keyword"
            class="am-search"
            size="small"
            clearable
            :placeholder="t('assetMarket.searchPlaceholder')"
            @keyup.enter="loadMarket()"
            @clear="loadMarket()"
          >
            <template #prefix><el-icon><Search /></el-icon></template>
          </el-input>
        </div>

        <div v-if="loading && !market.length" class="am-hint">{{ t('common.loading') }}</div>
        <div v-else-if="loadError" class="am-hint am-hint--error">{{ loadError }}</div>
        <el-empty
          v-else-if="!market.length"
          :description="t('assetMarket.emptyMarket')"
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
              <el-tag v-if="a.installed" size="small" type="success" effect="plain">{{ t('assetMarket.tagInstalled') }}</el-tag>
              <el-tag v-if="a.upgradable" size="small" type="warning" effect="plain">{{ t('assetMarket.tagUpgradable') }}</el-tag>
            </div>
            <div class="am-desc">{{ a.description || t('assetMarket.noDescription') }}</div>
            <div class="am-meta">
              <span>{{ t('assetMarket.filesAndSize', { n: a.file_count, size: fmtSize(a.total_size) }) }}</span>
              <span v-if="depsSummary(a.deps)" class="am-meta-deps">{{ depsSummary(a.deps) }}</span>
            </div>
            <div class="am-meta">{{ listerLabel(a) }}</div>
            <div class="am-card-actions">
              <el-button size="small" @click="openDetail(a)">{{ t('assetMarket.detail') }}</el-button>
              <el-button size="small" type="primary" @click="openInstall(a)">{{ t('assetAction.install') }}</el-button>
            </div>
          </div>
        </div>
        <!-- 分页：只给「加载更多」，不做页码条——顺序是 updated_at，翻页找比搜索慢 -->
        <div v-if="hasMore" class="am-more">
          <el-button size="small" :loading="loadingMore" @click="loadMoreMarket">
            {{ t('assetMarket.loadMore', { shown: market.length, total: marketTotal }) }}
          </el-button>
        </div>
      </div>

      <!-- ===== 远端市场（pull-through） ===== -->
      <!-- 与「本地市场」的区别：这里的东西**还没进本机**。平台只配了「这个租户能看哪个站」，
           不替租户逐条导入；点「拉取并安装」那一刻才下载到本机，并按对方的 install_policy
           决定是直接可用，还是先进本机待审（等平台审核通过）。 -->
      <div v-show="tab === 'remote'" class="am-pane">
        <div class="am-tip">{{ t('assetMarket.remoteTip') }}</div>
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
            :placeholder="t('assetMarket.peerAll')"
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
            :placeholder="t('assetMarket.searchPlaceholder')"
            @keyup.enter="loadRemote()"
            @clear="loadRemote()"
          >
            <template #prefix><el-icon><Search /></el-icon></template>
          </el-input>
        </div>

        <div v-if="remoteLoading && !remotePeers.length" class="am-hint">{{ t('common.loading') }}</div>
        <div v-else-if="remoteError" class="am-hint am-hint--error">{{ remoteError }}</div>
        <el-empty
          v-else-if="!remotePeers.length"
          :description="t('assetMarket.remoteEmpty')"
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
              >{{ p.install_policy === 'auto' ? t('assetMarket.policyAuto') : t('assetMarket.policyReview') }}</el-tag>
              <span class="am-cell-sub">
                {{ t('assetMarket.peerAssets', { n: p.count }) }}<template
                  v-if="p.truncated">{{ t('assetMarket.peerTruncated', { shown: p.items.length }) }}</template>
              </span>
              <span class="am-cell-sub am-remote-fold">
                {{ collapsedPeers[p.peer] ? t('common.expand') : t('common.collapse') }}
              </span>
            </div>
            <!-- 错误不跟着折叠：站挂了还要能一眼看到原因 -->
            <div v-if="p.error" class="am-hint am-hint--error">{{ p.error }}</div>
            <div v-else-if="collapsedPeers[p.peer]" class="am-cell-sub am-remote-folded">
              {{ t('assetMarket.peerFolded', { n: p.items.length }) }}
            </div>
            <div v-else-if="!p.items.length" class="am-cell-sub">{{ t('assetMarket.peerEmpty') }}</div>
            <div v-else class="am-cards">
              <div v-for="a in p.items" :key="p.peer + ':' + a.id" class="am-card">
                <div class="am-card-title">
                  <span class="am-kind" :class="'am-kind--' + a.kind">{{ kindLabel(a.kind) }}</span>
                  <span class="am-name" :title="a.display_name || a.name">{{ a.display_name || a.name }}</span>
                  <span class="am-ver">v{{ a.build }}</span>
                </div>
                <div v-if="a.local || a.installed" class="am-badges">
                  <el-tag v-if="a.local" size="small" type="info" effect="plain">
                    {{ t('assetMarket.tagPulled', { build: a.local.build }) }}
                  </el-tag>
                  <el-tag v-if="a.local && a.local.status === 'pending'" size="small" type="warning" effect="plain">
                    {{ t('assetMarket.tagPendingReview') }}
                  </el-tag>
                  <el-tag v-if="a.installed" size="small" type="success" effect="plain">
                    {{ t('assetMarket.tagInstalled') }}<template v-if="a.installed_build"> v{{ a.installed_build }}</template>
                  </el-tag>
                  <el-tag v-if="a.stale" size="small" type="warning" effect="plain">{{ t('assetMarket.tagStale') }}</el-tag>
                </div>
                <div class="am-desc">{{ a.description || t('assetMarket.remoteNoDescription') }}</div>
                <div class="am-meta">
                  <span>{{ t('assetMarket.filesAndSize', { n: a.file_count, size: fmtSize(a.total_size) }) }}</span>
                  <span v-if="depsSummary(a.deps)" class="am-meta-deps">{{ depsSummary(a.deps) }}</span>
                </div>
                <div class="am-meta">{{ t('assetMarket.listerSource', { peer: p.peer }) }}</div>
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
        <div class="am-tip">{{ t('assetMarket.mineTip') }}</div>
        <div class="am-toolbar">
          <el-button
            size="small"
            type="danger"
            plain
            :disabled="!mine.some((m) => m.status !== 'listed')"
            @click="clearMine"
          >{{ t('assetMarket.clearUnlisted') }}</el-button>
        </div>
        <el-table v-loading="loading" :data="mine" size="small" style="width: 100%">
          <el-table-column :label="t('assetMarket.colName')" min-width="180">
            <template #default="{ row }">
              <div class="am-cell-name">{{ row.display_name || row.name }}</div>
              <div class="am-cell-sub">{{ row.name }}</div>
            </template>
          </el-table-column>
          <el-table-column :label="t('assetMarket.colKind')" width="110">
            <template #default="{ row }">
              <span class="am-kind" :class="'am-kind--' + row.kind">{{ kindLabel(row.kind) }}</span>
            </template>
          </el-table-column>
          <el-table-column :label="t('assetMarket.colStatus')" width="120">
            <template #default="{ row }">
              <el-tooltip v-if="row.review_note" :content="row.review_note" placement="top">
                <el-tag size="small" :type="statusTagType(row.status)" effect="plain">{{ statusLabel(row.status) }}</el-tag>
              </el-tooltip>
              <el-tag v-else size="small" :type="statusTagType(row.status)" effect="plain">{{ statusLabel(row.status) }}</el-tag>
            </template>
          </el-table-column>
          <el-table-column :label="t('assetMarket.colBuild')" width="70">
            <template #default="{ row }">v{{ row.build }}</template>
          </el-table-column>
          <el-table-column :label="t('assetMarket.colVisibility')" width="100">
            <template #default="{ row }">{{ row.visibility === 'allowlist' ? t('assetMarket.visibilityAllowlist') : t('assetMarket.visibilityAll') }}</template>
          </el-table-column>
          <el-table-column :label="t('assetMarket.colUpdated')" width="160">
            <template #default="{ row }">{{ fmtTime(row.updated_at) }}</template>
          </el-table-column>
          <el-table-column :label="t('assetMarket.colActions')" width="140" fixed="right">
            <template #default="{ row }">
              <el-button
                v-if="row.status === 'pending'"
                link
                type="primary"
                size="small"
                @click="withdraw(row)"
              >{{ t('assetMarket.withdraw') }}</el-button>
              <!-- 已通过的不能自己删：可能已经被别的租户装走，先让平台下架 -->
              <el-button
                v-if="row.status !== 'listed'"
                link
                type="danger"
                size="small"
                @click="removeMine(row)"
              >{{ t('common.delete') }}</el-button>
              <span v-else class="am-cell-sub">{{ t('assetMarket.kickedByPlatform') }}</span>
            </template>
          </el-table-column>
        </el-table>
      </div>

      <!-- ===== 安装记录 ===== -->
      <div v-show="tab === 'installs'" class="am-pane">
        <div class="am-tip">{{ t('assetMarket.installsTip') }}</div>
        <div class="am-toolbar">
          <el-button
            size="small"
            type="danger"
            plain
            :disabled="!installs.length"
            @click="clearInstalls"
          >{{ t('assetMarket.clearInstalls') }}</el-button>
        </div>
        <el-table v-loading="loading" :data="installs" size="small" style="width: 100%">
          <el-table-column :label="t('assetMarket.colAsset')" min-width="160">
            <template #default="{ row }">
              <div class="am-cell-name">{{ row.asset_name }}</div>
              <div class="am-cell-sub">{{ t('assetMarket.colSource', { schema: row.source_schema }) }}</div>
            </template>
          </el-table-column>
          <el-table-column :label="t('assetMarket.colKind')" width="110">
            <template #default="{ row }">
              <span class="am-kind" :class="'am-kind--' + row.kind">{{ kindLabel(row.kind) }}</span>
            </template>
          </el-table-column>
          <el-table-column :label="t('assetMarket.colInstalledAs')" min-width="140" prop="installed_name" />
          <el-table-column :label="t('assetMarket.colBuild')" width="120">
            <template #default="{ row }">
              v{{ row.build }}
              <el-tag v-if="row.upgradable" size="small" type="warning" effect="plain">
                {{ t('assetMarket.colLatest', { build: row.latest_build }) }}
              </el-tag>
            </template>
          </el-table-column>
          <el-table-column :label="t('assetMarket.colInstaller')" width="130">
            <template #default="{ row }">{{ row.installed_by_name || row.installed_by || '—' }}</template>
          </el-table-column>
          <el-table-column :label="t('assetMarket.colInstalledAt')" width="160">
            <template #default="{ row }">{{ fmtTime(row.installed_at) }}</template>
          </el-table-column>
          <el-table-column :label="t('assetMarket.colActions')" width="90" fixed="right">
            <template #default="{ row }">
              <el-button size="small" link type="danger" @click="removeInstall(row)">{{ t('common.delete') }}</el-button>
            </template>
          </el-table-column>
        </el-table>
      </div>
    </div>

    <!-- ===== 详情 ===== -->
    <el-dialog v-model="detailVisible" :title="detail?.display_name || detail?.name || t('assetMarket.detailTitle')" width="620px">
      <div v-if="detail" class="am-detail">
        <div class="am-detail-row">
          <span class="am-detail-key">{{ t('assetMarket.detailKind') }}</span>
          <span class="am-kind" :class="'am-kind--' + detail.kind">{{ kindLabel(detail.kind) }}</span>
          <span class="am-ver">v{{ detail.build }}</span>
        </div>
        <div class="am-detail-row">
          <span class="am-detail-key">
            {{ detail.listed_by?.origin?.server ? t('assetMarket.detailSource') : t('assetMarket.detailLister') }}
          </span>
          <span>
            <template v-if="detail.listed_by?.origin?.server">
              {{ t('assetMarket.detailImportedFrom', { peer: detail.listed_by.origin.server }) }}
              <span v-if="detail.listed_by.origin.imported_at" class="am-detail-sub">
                · {{ fmtTime(detail.listed_by.origin.imported_at) }}
              </span>
            </template>
            <template v-else>{{ listerLabel(detail) }}</template>
          </span>
        </div>
        <div class="am-detail-row">
          <span class="am-detail-key">{{ t('assetMarket.detailContent') }}</span>
          <span>{{ t('assetMarket.filesAndSize', { n: detail.file_count, size: fmtSize(detail.total_size) }) }}<template
            v-if="detail.entry">{{ t('assetMarket.detailEntry', { entry: detail.entry }) }}</template></span>
        </div>
        <div v-if="detail.description" class="am-detail-row">
          <span class="am-detail-key">{{ t('assetMarket.detailDesc') }}</span>
          <span class="am-detail-desc">{{ detail.description }}</span>
        </div>
        <div class="am-detail-row am-detail-row--top">
          <span class="am-detail-key">{{ t('assetMarket.detailDeps') }}</span>
          <div class="am-detail-block">
            <div v-if="!detail.deps || !hasDeps(detail.deps)" class="am-cell-sub">{{ t('assetMarket.detailNoDeps') }}</div>
            <template v-else>
              <div v-if="detail.deps.skills?.length">{{ t('assetMarket.detailSkills') }}
                <el-tag v-for="s in detail.deps.skills" :key="s" size="small" class="am-dep-tag">{{ s }}</el-tag>
              </div>
              <div v-if="detail.deps.private_skills?.length" class="am-cell-sub">
                {{ t('assetMarket.detailPrivateSkills', { names: detail.deps.private_skills.join(t('assetDep.itemJoiner')) }) }}
              </div>
              <div v-if="detail.deps.tools?.length">{{ t('assetMarket.detailTools') }}
                <el-tag v-for="tool in detail.deps.tools" :key="tool.type + tool.ref" size="small" class="am-dep-tag">
                  {{ tool.ref }}<span v-if="tool.grant_kind"> · {{ tool.grant_kind }}</span>
                </el-tag>
              </div>
              <div v-if="detail.deps.requires?.length">{{ t('assetMarket.detailRequires') }}
                <el-tag v-for="r in detail.deps.requires" :key="r" size="small" type="warning" class="am-dep-tag">{{ r }}</el-tag>
              </div>
              <div v-if="detail.deps.egress">{{ t('assetMarket.detailEgress') }}</div>
            </template>
          </div>
        </div>
        <div v-if="detail.review_note" class="am-detail-row am-detail-row--top">
          <span class="am-detail-key">{{ t('assetMarket.detailReviewNote') }}</span>
          <span class="am-detail-desc">{{ detail.review_note }}</span>
        </div>
      </div>
      <template #footer>
        <el-button @click="detailVisible = false">{{ t('common.close') }}</el-button>
        <el-button v-if="detail" type="primary" @click="installFromDetail">{{ t('assetAction.install') }}</el-button>
      </template>
    </el-dialog>

    <!-- ===== 安装 ===== -->
    <el-dialog v-model="installVisible"
               :title="t('assetMarket.installTitle', { name: installing?.display_name || installing?.name || '' })"
               width="520px">
      <div v-if="installing && !installDone" class="am-form">
        <div class="am-form-item">
          <label>{{ t('assetMarket.installName') }}</label>
          <el-input v-model="installForm.name" size="small"
                    :placeholder="t('assetMarket.installNamePlaceholder')" />
          <div class="am-form-hint">{{ t('assetMarket.installNameHint') }}</div>
        </div>
        <!-- 预检：与「导入」共用同一个渲染组件（同名提示 / 分组依赖 / 随包自带 / notes） -->
        <div v-if="installInspectLoading" class="am-form-hint">{{ t('assetMarket.installInspecting') }}</div>
        <AssetDepCheck v-else-if="installInspect" :inspect="installInspect"
                       :kind-label="kindLabelOf(installing.kind)" verb="install" />

        <!-- 只有自定义工具有「停用」这一态（工作流只有正常 / 废弃，装完是正常） -->
        <div v-if="installing.kind === 'custom_tool'" class="am-form-item">
          <label>{{ t('assetMarket.installStatus') }}</label>
          <el-radio-group v-model="installForm.status" size="small">
            <el-radio :value="1">{{ t('assetMarket.installStatusOn') }}</el-radio>
            <el-radio :value="2">{{ t('assetMarket.installStatusOff') }}</el-radio>
          </el-radio-group>
          <div class="am-form-hint">{{ t('assetMarket.installStatusHint') }}</div>
        </div>

        <div class="am-form-item">
          <el-checkbox v-model="installForm.publish">{{ t('assetMarket.installPublishNow') }}</el-checkbox>
          <div class="am-form-hint">{{ t('assetMarket.installPublishHint') }}</div>
        </div>
        <div v-if="installConflict" class="am-warn">{{ installConflict }}</div>
        <!-- 失败一律内联，不用 toast：改名 / 前置条件不满足时要能看着提示原地重试 -->
        <el-alert v-if="installError" :title="installError" type="error" :closable="false" show-icon />
        <div v-if="installing.kind === 'agent'" class="am-form-hint">{{ t('assetMarket.installAgentHint') }}</div>
        <div v-if="installing.kind === 'workflow'" class="am-form-hint">{{ t('assetMarket.installWorkflowHint') }}</div>
      </div>

      <!-- 结果页与「导入」共用同一个组件：成功行 + 注意事项 + 跳过文件，常驻到用户点完成 -->
      <AssetInstallResult v-if="installDone" :name="installDone.name" :overwritten="installDone.overwritten"
                          :warnings="installDone.warnings" :skipped="installDone.skipped"
                          :published="installDone.published" verb="install" />

      <template #footer>
        <template v-if="installDone">
          <el-button type="primary" @click="installVisible = false">{{ t('assetTransfer.gotIt') }}</el-button>
        </template>
        <template v-else>
          <el-button @click="installVisible = false">{{ t('common.cancel') }}</el-button>
          <el-button type="primary" :loading="submitting" @click="submitInstall">{{ t('assetAction.install') }}</el-button>
        </template>
      </template>
    </el-dialog>

    <!-- ===== 上架 ===== -->
    <el-dialog v-model="listingVisible" :title="t('assetMarket.listingTitle')" width="560px">
      <div class="am-form">
        <div class="am-form-item">
          <label>{{ t('assetMarket.listingKind') }}</label>
          <el-radio-group v-model="listingForm.kind" size="small" @change="onExposeKindChange">
            <el-radio value="skill">{{ t('assetImport.kindSkill') }}</el-radio>
            <el-radio value="custom_tool">{{ t('assetImport.kindCustomTool') }}</el-radio>
            <el-radio value="agent">{{ t('assetImport.kindAgent') }}</el-radio>
            <el-radio value="workflow">{{ t('assetImport.kindWorkflow') }}</el-radio>
          </el-radio-group>
        </div>
        <div class="am-form-item">
          <label>{{ t('assetMarket.listingPick') }}</label>
          <el-select
            v-model="listingForm.ref"
            size="small"
            filterable
            :loading="entityLoading"
            :placeholder="t('assetMarket.listingPickPlaceholder')"
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
          <div class="am-form-hint">{{ t('assetMarket.listingHint') }}</div>
        </div>
        <div class="am-form-hint">{{ t('assetMarket.listingVisibleHint') }}</div>
        <div v-if="listingWarnings.length" class="am-warn">
          <div v-for="(w, i) in listingWarnings" :key="i">{{ w }}</div>
        </div>
      </div>
      <template #footer>
        <el-button @click="listingVisible = false">{{ t('common.close') }}</el-button>
        <el-button type="primary" :loading="submitting" :disabled="!listingForm.ref" @click="submitListing">
          {{ t('assetMarket.listingSubmit') }}
        </el-button>
      </template>
    </el-dialog>
  </div>
</template>

<script setup>
import { ref, computed, onMounted, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { ElMessage, ElMessageBox } from 'element-plus'
import { Shop, Upload, Download, Refresh, Plus, Search, Connection, ArrowRight } from '@element-plus/icons-vue'
import adminApi from '../config/admin.js'
import { getToken } from '../utils/auth.js'
import { publishAssetVersion, ASSET_KIND_SUFFIX } from '../utils/assetTransfer.js'
import AssetDepCheck from './AssetDepCheck.vue'
import AssetInstallResult from './AssetInstallResult.vue'
import { formatError } from '../../locales'

const { t } = useI18n()

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
// 四类资产的类型名与「导入」共用同一套词（assetImport.kind*），不另写一份。
// 顺序沿用技能 → 工具 → 智能体 → 工作流，别跟着 key 表的顺序走
const KIND_FILTER_ORDER = ['skill', 'custom_tool', 'agent', 'workflow']
const kindFilters = computed(() => [
  { value: '', label: t('assetMarket.filterAll') },
  ...KIND_FILTER_ORDER.map((k) => ({ value: k, label: t(`assetImport.kind${ASSET_KIND_SUFFIX[k]}`) }))
])

const kindLabel = (k) => (ASSET_KIND_SUFFIX[k] ? t(`assetImport.kind${ASSET_KIND_SUFFIX[k]}`) : k || '—')

const STATUS_LABEL_KEYS = {
  draft: 'statusDraft',
  pending: 'statusPending',
  listed: 'statusListed',
  rejected: 'statusRejected',
  delisted: 'statusDelisted'
}
const statusLabel = (s) => (STATUS_LABEL_KEYS[s] ? t(`assetMarket.${STATUS_LABEL_KEYS[s]}`) : s || '—')
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
  if (src) return t('assetMarket.listerSource', { peer: src })
  const tenant = a?.listed_by?.tenant?.display_name || a?.listed_by?.tenant?.schema || ''
  const user = a?.listed_by?.user?.display_name || ''
  if (user && tenant) return t('assetMarket.listerUserTenant', { user, tenant })
  if (tenant) return t('assetMarket.listerTenant', { tenant })
  return ''
}

const hasDeps = (d) =>
  !!(d && ((d.skills && d.skills.length) || (d.private_skills && d.private_skills.length) ||
    (d.tools && d.tools.length) || (d.requires && d.requires.length) || d.egress))

const depsSummary = (d) => {
  if (!d) return ''
  const parts = []
  if (d.skills?.length) parts.push(t('assetMarket.depsSkills', { n: d.skills.length }))
  if (d.tools?.length) parts.push(t('assetMarket.depsTools', { n: d.tools.length }))
  if (d.requires?.length) parts.push(t('assetMarket.depsRequires', { list: d.requires.join('/') }))
  if (d.egress) parts.push(t('assetMarket.depsEgress'))
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
    if (body.code !== 200) throw new Error(formatError(body) || t('assetMarket.loadListFailed'))
    const items = body.data?.items || []
    market.value = append ? market.value.concat(items) : items
    marketTotal.value = body.data?.total ?? items.length
    hasMore.value = !!body.data?.has_more
    // 来源选项由后端汇总（忽略 origin 条件），筛了某个来源时其它选项不会消失
    if (!append) origins.value = body.data?.origins || []
  } catch (e) {
    if (append) ElMessage.error(e.message || t('assetMarket.loadMoreFailed'))
    else {
      loadError.value = e.message || t('assetMarket.loadListFailed')
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
    if (body.code !== 200) throw new Error(formatError(body) || t('assetMarket.loadRemoteFailed'))
    // 单站不可达不影响其它站：error 是每个 peer 自己的字段，原样展示
    remotePeers.value = (body.data?.peers || []).filter((p) => p.items?.length || p.error)
    // 站点选项只从「全部」那次取：切到单站后返回的只有一站，拿它更新会把其它选项弄丢。
    // 连名带 url 一起存：下拉项要显示地址，而 v-model 的值仍必须是站名（后端按名过滤）
    if (!peer) {
      remotePeerOptions.value = remotePeers.value.map((p) => ({ peer: p.peer, url: p.url || '' }))
    }
    remoteLoaded.value = true
  } catch (e) {
    remoteError.value = e.message || t('assetMarket.loadRemoteFailed')
    remotePeers.value = []
  } finally {
    remoteLoading.value = false
  }
}

// 按钮文案：本机已有就直接装，等审核就只是提示，远端有新的快照则重拉
const pullActionLabel = (a) => {
  if (a.stale) return t('assetMarket.pullStale')
  if (!a.local) return t('assetMarket.pullInstall')
  if (a.local.status === 'pending') return t('assetMarket.pullPending')
  return a.installed ? t('assetMarket.pullReinstall') : t('assetAction.install')
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
    ElMessage.info(t('assetMarket.pulledPending'))
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
            t('assetMarket.conflictBody', { name: cf.name, build: cf.build, status: statusLabel(cf.status) }),
            t('assetMarket.conflictTitle'),
            {
              type: 'warning',
              confirmButtonText: t('assetMarket.conflictConfirm'),
              cancelButtonText: t('common.cancel')
            }
          )
        } catch {
          return
        }
        return pullAndInstall(p, a, true)
      }
      throw new Error(formatError(body) || t('assetMarket.pullFailed'))
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
      ElMessage.success({ message: t('assetMarket.pulledReview'), duration: 2400 })
    }
  } catch (e) {
    ElMessage.error(e.message || t('assetMarket.pullFailed'))
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
  if (body.code !== 200) throw new Error(formatError(body) || t('assetMarket.loadMineFailed'))
  mine.value = body.data?.items || []
}

const loadInstalls = async () => {
  const res = await fetch(adminApi.share.installs, { headers: authHeaders() })
  const body = await res.json().catch(() => ({}))
  if (body.code !== 200) throw new Error(formatError(body) || t('assetMarket.loadInstallsFailed'))
  installs.value = body.data?.items || []
}

// 一键清空：一次 DELETE，成功与否都要把市场和记录一起刷（「已安装」标记来自这张表）
const clearInstalls = async () => {
  try {
    await ElMessageBox.confirm(
      t('assetMarket.clearInstallsBody', { n: installs.value.length }),
      t('assetMarket.clearInstallsTitle'),
      {
        type: 'warning',
        confirmButtonText: t('assetMarket.clearAll'),
        cancelButtonText: t('common.cancel')
      }
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
    if (body.code !== 200) throw new Error(formatError(body) || t('assetMarket.clearFailed'))
    ElMessage.success(t('assetMarket.clearedInstalls', { n: body.data?.deleted ?? 0 }))
    await Promise.all([loadInstalls(), loadMarket()])
  } catch (e) {
    ElMessage.error(e.message || t('assetMarket.clearFailed'))
  }
}

// 删安装记录：只抹掉这条流水，已装进租户的技能/工具/智能体都不动
const removeInstall = async (row) => {
  try {
    await ElMessageBox.confirm(
      t('assetMarket.removeInstallBody', { asset: row.asset_name, name: row.installed_name }),
      t('assetMarket.removeInstallTitle'),
      {
        type: 'warning',
        confirmButtonText: t('assetMarket.removeRecord'),
        cancelButtonText: t('common.cancel')
      }
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
    if (body.code !== 200) throw new Error(formatError(body) || t('assetMarket.deleteFailed'))
    ElMessage.success(t('assetMarket.removedInstall'))
    // 「已安装」标记来自这张表，市场列表要一起刷
    await Promise.all([loadInstalls(), loadMarket()])
  } catch (e) {
    ElMessage.error(e.message || t('assetMarket.deleteFailed'))
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
    ElMessage.error(e.message || t('assetMarket.loadFailed'))
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
    if (body.code !== 200) throw new Error(formatError(body) || t('assetMarket.detailLoadFailed'))
    detail.value = body.data?.asset || a
  } catch (e) {
    ElMessage.warning(e.message || t('assetMarket.detailLoadSoft'))
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

// 市场列表与安装弹窗用同一套类型名；不认识的 kind 落到「资产」而不是原样露出编码值
const kindLabelOf = (k) => (ASSET_KIND_SUFFIX[k] ? kindLabel(k) : t('assetDep.defaultKind'))

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
    installInspect.value = body.code === 200 ? body.data || null : { error: formatError(body) || t('assetMarket.inspectFailed') }
  } catch (e) {
    if (seq === inspectSeq) installInspect.value = { error: e.message || t('assetMarket.inspectDepFailed') }
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

    // 同名不是失败，而是「要你确认」：与上传导入同一套话术（assetImport.overwrite* 共用）
    const suffix = ASSET_KIND_SUFFIX[a.kind]
    if (body.code !== 200 && body.data?.can_overwrite && suffix) {
      try {
        await ElMessageBox.confirm(
          `${formatError(body)}。${t(`assetImport.overwriteDetail${suffix}`)}`,
          t(`assetImport.overwriteTitle${suffix}`),
          {
            type: 'warning',
            confirmButtonText: t('assetImport.overwrite'),
            cancelButtonText: t('common.cancel')
          }
        )
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
        installConflict.value = t('assetMarket.conflictSuggested', {
          msg: formatError(body) || t('assetMarket.nameTaken'),
          name: suggested
        })
        return
      }
      throw new Error(formatError(body) || t('assetMarket.installFailed'))
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
        warnings.push(t('assetTransfer.publishFailedInline', { verb: t('assetAction.install'), msg: e.message }))
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
    installError.value = e.message || t('assetMarket.installFailed')
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
      if (body.code !== 200) throw new Error(formatError(body) || t('assetMarket.readSkillsFailed'))
      entityOptions.value = (body.data?.items || [])
        // 私有技能不能单独上架（后端会拒绝）：正文住在所属智能体的目录里，只能随智能体走，
        // 否则装到别的租户只剩一个丢了归属的租户级副本
        .filter((s) => !s.agent_id)
        .map((s) => ({
          ref: s.id,
          label: s.name,
          note: s.protected ? t('assetMarket.noteBuiltin')
            : Number(s.status) === 3 ? t('assetMarket.noteDeprecated') : ''
        }))
    } else if (k === 'custom_tool') {
      const res = await fetch(adminApi.customTools.list, { headers: authHeaders() })
      const body = await res.json().catch(() => ({}))
      if (body.code !== 200) throw new Error(formatError(body) || t('assetMarket.readToolsFailed'))
      entityOptions.value = (body.data?.tools || []).map((tool) => ({
        ref: tool.name,
        label: tool.name,
        note: Number(tool.status) === 3 ? t('assetMarket.noteDeprecated') : ''
      }))
    } else if (k === 'workflow') {
      // 工作流没有目录，只有 {schema}.flows 里的一行；上架时后端把它序列化成 FLOW.json
      const res = await fetch(adminApi.flows.base, {
        method: 'POST',
        headers: authHeaders(),
        body: JSON.stringify({ page: 1, limit: 500 })
      })
      const body = await res.json().catch(() => ({}))
      if (body.code !== 200) throw new Error(formatError(body) || t('assetMarket.readFlowsFailed'))
      entityOptions.value = (body.data?.items || []).map((f) => ({
        ref: f.id,
        label: f.name,
        note: Number(f.status) === 3 ? t('assetMarket.noteDeprecated') : ''
      }))
    } else {
      const res = await fetch(adminApi.agents.base, {
        method: 'POST',
        headers: authHeaders(),
        body: JSON.stringify({ page: 1, limit: 500 })
      })
      const body = await res.json().catch(() => ({}))
      if (body.code !== 200) throw new Error(formatError(body) || t('assetMarket.readAgentsFailed'))
      entityOptions.value = (body.data?.items || []).map((a) => ({
        ref: a.id,
        label: a.name,
        note: a.is_supervisor ? t('assetMarket.noteSupervisor')
          : a.protected ? t('assetMarket.noteBuiltin')
          : Number(a.status) === 3 ? t('assetMarket.noteDeprecated') : ''
      }))
    }
  } catch (e) {
    ElMessage.error(e.message || t('assetMarket.readEntitiesFailed'))
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
    if (body.code !== 200) throw new Error(formatError(body) || t('assetMarket.listFailed'))
    const warnings = body.data?.warnings || []
    listingWarnings.value = warnings
    // need_review=false 表示该租户免审、这条已经直接上架（缺省即如此），文案要跟着变
    ElMessage.success({
      message: body.data?.need_review === false ? t('assetMarket.listedOk') : t('assetMarket.submittedOk'),
      duration: 1800
    })
    tab.value = 'mine'
    await Promise.all([loadMine(), loadMarket()])
    if (!warnings.length) listingVisible.value = false
  } catch (e) {
    ElMessage.error(e.message || t('assetMarket.listFailed'))
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
      t('assetMarket.deleteAssetBody', { name: row.name, status: statusLabel(row.status) }),
      t('assetMarket.deleteAssetTitle'),
      {
        type: 'warning',
        confirmButtonText: t('common.delete'),
        cancelButtonText: t('common.cancel')
      }
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
    if (body.code !== 200) throw new Error(formatError(body) || t('assetMarket.deleteFailed'))
    ElMessage.success(t('assetMarket.deleted'))
    await Promise.all([loadMine(), loadMarket()])
  } catch (e) {
    ElMessage.error(e.message || t('assetMarket.deleteFailed'))
  }
}

const clearMine = async () => {
  const removable = mine.value.filter((m) => m.status !== 'listed').length
  try {
    await ElMessageBox.confirm(
      t('assetMarket.clearMineBody', { n: removable }),
      t('assetMarket.clearMineTitle'),
      {
        type: 'warning',
        confirmButtonText: t('assetMarket.clearAll'),
        cancelButtonText: t('common.cancel')
      }
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
    if (body.code !== 200) throw new Error(formatError(body) || t('assetMarket.clearFailed'))
    ElMessage.success(t('assetMarket.clearedAssets', { n: body.data?.deleted ?? 0 }))
    await Promise.all([loadMine(), loadMarket()])
  } catch (e) {
    ElMessage.error(e.message || t('assetMarket.clearFailed'))
  }
}

// ===== 撤回 =====
const withdraw = async (row) => {
  const isDraft = row.status === 'draft'
  try {
    await ElMessageBox.confirm(
      isDraft
        ? t('assetMarket.draftDeleteBody', { name: row.name })
        : t('assetMarket.withdrawBody', { name: row.name }),
      isDraft ? t('assetMarket.draftDeleteTitle') : t('assetMarket.withdrawTitle'),
      {
        type: 'warning',
        confirmButtonText: isDraft ? t('common.delete') : t('assetMarket.withdraw'),
        cancelButtonText: t('common.cancel')
      }
    )
  } catch {
    return
  }
  try {
    const res = await fetch(adminApi.share.withdraw(row.id), { method: 'POST', headers: authHeaders() })
    const body = await res.json().catch(() => ({}))
    if (body.code !== 200) throw new Error(formatError(body) || t('assetMarket.opFailed'))
    ElMessage.success({ message: isDraft ? t('assetMarket.deleted') : t('assetMarket.withdrawn'), duration: 1500 })
    await Promise.all([loadMine(), loadMarket()])
  } catch (e) {
    ElMessage.error(e.message || t('assetMarket.opFailed'))
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
