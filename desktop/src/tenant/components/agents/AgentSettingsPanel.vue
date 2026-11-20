<template>
  <!-- 智能体设置面板：调用授权 / 工具配置两个 tab + 工具授权侧栏 + 生效预览 -->
        <!-- 设置面板：调用授权 / 工具配置（样式对齐工作流试运行面板）
             teleport 到 body + fixed：避免被内容区/编辑区容器的 overflow:hidden 裁剪
             （会话窗口占宽、内容区比面板窄时会"看起来被会话窗口盖住"） -->
        <teleport to="body">
        <div v-if="visible" ref="settingsPanelRef" class="settings-panel" :style="settingsPanelStyle">
          <div class="panel-resizer" @mousedown="startSettingsResize"></div>
          <div class="panel-header">
            <div class="settings-tabs">
              <button class="settings-tab-btn" :class="{ active: settingsTab === 'relations' }"
                      @click="switchSettingsTab('relations')">{{ $t('agentSettings.tabRelations') }}</button>
              <button class="settings-tab-btn" :class="{ active: settingsTab === 'tools' }"
                      @click="switchSettingsTab('tools')">{{ $t('agentSettings.tabTools') }}</button>
            </div>
            <span class="close-icon" @click="$emit('close')">&#xd7;</span>
          </div>
          <div class="panel-body">
            <!-- tab 1：调用授权 -->
            <template v-if="settingsTab === 'relations'">
              <div class="settings-field">
                <div class="field-label">{{ $t('agentSettings.skillsLabel') }}</div>
                <!-- 与「工具授权」同一交互：点按钮从左侧拉出选择面板，点一项即绑定 / 解绑（改动即时保存） -->
                <div class="bind-row">
                  <el-button size="small" :disabled="!agentId || locked" @click="openSkillPicker">
                    {{ $t('agentSettings.selectSkills') }}
                  </el-button>
                  <div class="bind-tags">
                    <el-tag v-for="id in boundSkillIds" :key="id" size="small"
                            :closable="!locked" :disable-transitions="true" @close="unbindSkill(id)">
                      {{ skillName(id) }}
                    </el-tag>
                    <span v-if="!boundSkillIds.length" class="bind-tags-empty">{{ $t('agentSettings.noSkills') }}</span>
                  </div>
                </div>
                <div class="editor-tip">{{ $t('agentSettings.skillsHint') }}</div>
                <div v-if="!agentId" class="editor-tip">{{ $t('agentSettings.needSaveSkills') }}</div>
              </div>

              <div class="settings-field">
                <div class="field-label">{{ $t('agentSettings.allowDiscovery') }}</div>
                <el-switch v-model="agent.skill_discovery"
                           :disabled="!agentId || locked"
                           @change="saveSkillDiscovery" />
                <div class="editor-tip">{{ $t('agentSettings.skillDiscoveryHint') }}</div>
                <div v-if="!agentId" class="editor-tip">{{ $t('agentSettings.needSaveDiscovery') }}</div>
              </div>

              <div class="settings-field">
                <div class="field-label">{{ $t('agentSettings.defaultLanguage') }}</div>
                <el-select v-model="defaultLanguage" size="small" style="width: 100%"
                           popper-class="agent-lang-select-popper"
                           :disabled="!agentId || locked" @change="saveDefaultLanguage">
                  <el-option v-for="opt in languageOptions" :key="opt.value" :label="opt.label" :value="opt.value" />
                </el-select>
                <div class="editor-tip">{{ $t('agentSettings.defaultLanguageHint') }}</div>
                <div v-if="!agentId" class="editor-tip">{{ $t('agentSettings.needSaveLanguage') }}</div>
              </div>

              <div class="settings-field">
                <div class="field-label">{{ $t('agentSettings.supervisorLabel') }}</div>
                <el-switch v-model="agent.is_supervisor"
                           :disabled="!agentId || locked"
                           @change="saveSupervisor" />
                <div class="editor-tip">{{ $t('agentSettings.supervisorHint') }}</div>
                <div class="editor-tip">{{ $t('agentSettings.supervisorGrantHint') }}</div>
                <div v-if="!agentId" class="editor-tip">{{ $t('agentSettings.needSaveSupervisor') }}</div>
              </div>

              <!-- 两个方向共用同一个左侧面板（标题随方向变），点一项即时授权 / 收回 -->
              <div class="settings-field">
                <div class="field-label">{{ $t('agentSettings.calledBy') }}</div>
                <div class="bind-row">
                  <el-button size="small" :disabled="!agentId || locked" @click="openAgentPicker('in')">
                    {{ $t('agentSettings.selectCaller') }}
                  </el-button>
                  <div class="bind-tags">
                    <el-tag v-for="id in agent.called_by_ids" :key="id" size="small"
                            :type="isAgentUnreleased(id) ? 'warning' : 'primary'"
                            :title="isAgentUnreleased(id) ? unreleasedTip : ''"
                            :closable="!locked" :disable-transitions="true" @close="unbindAgentCall('in', id)">
                      {{ agentName(id) }}
                    </el-tag>
                    <span v-if="!agent.called_by_ids.length" class="bind-tags-empty">{{ $t('agentSettings.nobodyCanCall') }}</span>
                  </div>
                </div>
              </div>
              <div class="settings-field">
                <div class="field-label">{{ $t('agentSettings.canCall') }}</div>
                <div class="bind-row">
                  <el-button size="small" :disabled="!agentId || locked" @click="openAgentPicker('out')">
                    {{ $t('agentSettings.selectCallee') }}
                  </el-button>
                  <div class="bind-tags">
                    <el-tag v-for="id in agent.can_call_ids" :key="id" size="small"
                            :type="isAgentUnreleased(id) ? 'warning' : 'primary'"
                            :title="isAgentUnreleased(id) ? unreleasedTip : ''"
                            :closable="!locked" :disable-transitions="true" @close="unbindAgentCall('out', id)">
                      {{ agentName(id) }}
                    </el-tag>
                    <span v-if="!agent.can_call_ids.length" class="bind-tags-empty">{{ $t('agentSettings.cannotCallAny') }}</span>
                  </div>
                </div>
              </div>
              <div v-if="supervisor" class="editor-tip">{{ $t('agentSettings.supervisorCallableTip') }}</div>
              <div v-else class="editor-tip">{{ $t('agentSettings.callDefaultTip') }}</div>
            </template>

            <!-- tab 2：工具配置（工具授权体系） -->
            <template v-else>
              <!-- 详情视图：在面板内用新视图覆盖配置内容（不弹窗），带返回按钮 -->
              <template v-if="previewVisible">
                <div class="view-back">
                  <el-button size="small" @click="previewVisible = false">{{ $t('agentSettings.previewBack') }}</el-button>
                  <span class="view-title">{{ $t('agentSettings.previewTitle') }}</span>
                  <el-button size="small" text class="view-refresh" @click="loadEffectiveInfo">{{ $t('agentSettings.refresh') }}</el-button>
                </div>
                <div v-if="!previewData" class="editor-tip">{{ $t('agentSettings.loading') }}</div>
                <template v-else>
                  <template v-if="previewData.no_grants && !previewData.builtin_default">
                    <div class="editor-tip">
                      {{ $t('agentSettings.previewNoGrantsPre') }}<strong>{{ $t('agentSettings.previewNoGrantsStrong') }}</strong>{{ $t('agentSettings.previewNoGrantsMid') }}<strong>{{ $t('agentSettings.previewNoGrantsStrong2') }}</strong>{{ $t('agentSettings.previewNoGrantsPost') }}
                    </div>
                  </template>
                  <template v-else>
                    <div class="editor-tip">
                      {{ $t('agentSettings.previewCount', { n: (previewData.final_tools || []).length }) }}
                      <template v-if="previewData.unrestricted">{{ $t('agentSettings.previewUnrestricted') }}</template>
                      <template v-if="previewData.degraded">{{ $t('agentSettings.previewDegraded') }}</template>
                    </div>
                    <div v-if="previewData.builtin_default" class="editor-tip">
                      {{ $t('agentSettings.previewBaselinePre') }}<strong>{{ $t('agentSettings.previewBaselineStrong') }}</strong>{{ $t('agentSettings.previewBaselinePost') }}
                    </div>
                    <div v-if="(previewData.final_tools || []).some(t => isExternalTool(t.name))" class="preview-warn">
                      {{ $t('agentSettings.previewExternalWarn') }}
                    </div>
                    <div v-for="w in (previewData.warnings || [])" :key="w" class="preview-warn">{{ w }}</div>
                    <!-- 按「来源组」分组展示：组标题 + 组内工具 -->
                    <div v-for="grp in previewGroups" :key="grp.ref" class="grant-section">
                      <div class="grant-section-title clickable" @click="togglePreviewCollapse(grp.ref)">
                        <el-icon class="picker-caret" :class="{ open: !isPreviewCollapsed(grp.ref) }"><CaretRight /></el-icon>
                        <el-icon class="grant-group-icon"><Folder /></el-icon>
                        {{ $t('agentSettings.groupItemsWithCount', { name: groupName(t, te, grp), n: grp.items.length }) }}
                      </div>
                      <div v-if="!isPreviewCollapsed(grp.ref)" class="tools-list">
                        <div v-for="t in grp.items" :key="t.ref" class="tool-item">
                          <el-icon class="grant-tool-icon"><Tools /></el-icon>
                          <div class="tool-item-main">
                            <div class="tool-name" :title="t.ref + (t.description ? ' · ' + t.description : '')">
                              {{ t.label || t.name }}
                              <span class="ref-tag">{{ t.name }}</span>
                              <span v-if="isExternalTool(t.name)" class="ref-tag warn">{{ $t('agentSettings.notExecuted') }}</span>
                              <span v-else-if="t.available === false" class="ref-tag warn"
                                    :title="unavailableTitle(t) || $t('agentSettings.unavailableTip')">{{ unavailableLabel(t.unavailable_kind) }}</span>
                            </div>
                            <div class="tool-desc" :title="t.description">{{ shortDesc(t) }}</div>
                          </div>
                        </div>
                      </div>
                    </div>
                    <div v-if="!previewGroups.length" class="editor-tip">{{ $t('agentSettings.noToolsInEffect') }}</div>
                  </template>
                </template>
              </template>

              <!-- 配置视图 -->
              <template v-else>
              <div v-if="!agentId" class="editor-tip">{{ $t('agentSettings.needSaveGrants') }}</div>

              <!-- supervisor：工具与其它 agent 同源 —— 常驻 = 「必须导入」里的 builtin 行 -->
              <div v-if="supervisor" class="editor-tip">
                {{ $t('agentSettings.grantToolsHintPre') }}<strong>{{ $t('agentSettings.grantToolsHintStrong') }}</strong>{{ $t('agentSettings.grantToolsHintMid') }}<span class="ref-tag core">core</span>{{ $t('agentSettings.grantToolsHintPost') }}
              </div>

              <div class="settings-field">
                <div class="field-label">{{ $t('agentSettings.unrestrictedLabel') }}</div>
                <el-switch v-model="grantDraft.unrestricted" :disabled="!agentId || locked"
                           @change="onUnrestrictedChange" />
                <div class="editor-tip">
                  <template v-if="grantDraft.unrestricted">{{ $t('agentSettings.unrestrictedOnHint') }}</template>
                  <template v-else>{{ $t('agentSettings.unrestrictedOffHint') }}</template>
                </div>
                <div v-if="previewData && !(previewData.candidate_refs || []).length" class="editor-tip">
                  {{ $t('agentSettings.noSkillToolDeps') }}
                </div>
              </div>

              <div class="tools-actions">
                <el-button size="small" type="primary" :disabled="!agentId || locked"
                           @click="openGrantPicker">{{ $t('agentSettings.addGrants') }}</el-button>
                <el-button size="small" :disabled="!agentId" @click="previewEffectiveTools">{{ $t('agentSettings.previewEffective') }}</el-button>
              </div>

              <div v-if="toolsLoading" class="editor-tip">{{ $t('agentSettings.grantsLoading') }}</div>

              <!-- 授权列表：这里刻意不用 v-if / v-else 链，两个独立 v-if，
                   否则后面的「生效摘要」会被并进同一条链（一条链只渲染一个分支，
                   表现为三段和保存条整块消失）。 -->
              <div v-if="!toolsLoading" class="tools-grants">
                <!-- 未配置授权时的说明：只是一条提示，不替代下面的三段 -->
                <div v-if="!grantDraft.items.length" class="tools-empty">
                  <div class="editor-tip">
                    <template v-if="supervisor">
                      {{ $t('agentSettings.noGrantsSupervisorPre') }}<strong>{{ $t('agentSettings.noGrantsSupervisorStrong') }}</strong>{{ $t('agentSettings.noGrantsSupervisorMid') }}<code>sql/main_core_tool_grants.sql</code>{{ $t('agentSettings.noGrantsSupervisorPost') }}
                    </template>
                    <template v-else>
                      {{ $t('agentSettings.noGrantsPlainPre') }}<strong>{{ $t('agentSettings.noGrantsPlainStrong') }}</strong>{{ $t('agentSettings.noGrantsPlainPost') }}
                    </template>
                  </div>
                  <el-button v-if="!supervisor" size="small" :disabled="!agentId || locked"
                             @click="importCurrentTools">
                    {{ $t('agentSettings.grantAllBuiltin') }}
                  </el-button>
                </div>

                <!-- 三段：必须导入 / 可使用 / 拒绝 —— 与左侧「工具授权」侧栏的三个区一一对应。
                     空区也照样渲染（显示「（空）」），否则零授权时三段整块消失，看起来像界面被改掉了。 -->
                <div v-for="k in grantKinds" :key="k" class="grant-section">
                  <div class="grant-section-title">{{ $t('agentSettings.groupItemsWithCount', { name: grantKindLabel(k), n: grantsByKind[k].length }) }}</div>
                  <div v-if="grantsByKind[k].length" class="tools-list">
                    <div v-for="g in grantsByKind[k]" :key="g.ref" class="tool-item">
                      <!-- 组 / 单个工具各用一个图标区分 -->
                      <el-icon v-if="g.item_type === 'group'" class="grant-group-icon"><Folder /></el-icon>
                      <el-icon v-else class="grant-tool-icon"><Tools /></el-icon>
                      <div class="tool-item-main">
                        <div class="tool-name">
                          {{ groupName(t, te, g) }}
                          <span class="ref-tag">{{ g.item_type === 'group' ? $t('agentSettings.tagGroup') : $t('agentSettings.tagTool') }}</span>
                          <span v-if="g.count" class="ref-tag">{{ $t('agentSettings.countItems', { n: g.count }) }}</span>
                          <span v-if="isMainCoreRef(g.ref)" class="ref-tag core"
                                :title="$t('agentSettings.mainCoreTitle')">core</span>
                          <span v-if="g.available === false" class="ref-tag warn"
                                :title="unavailableTitle(g) || $t('agentSettings.unavailableTip')">{{ unavailableLabel(g.unavailable_kind) }}</span>
                        </div>
                        <div class="tool-desc" :title="(groupDesc(t, te, g) ? groupDesc(t, te, g) + ' · ' : '') + g.ref">
                          {{ shortText(groupDesc(t, te, g)) || g.ref }}
                        </div>
                      </div>
                      <el-icon v-if="!locked" class="node-del"
                               @click="removeGrant(g.ref)"><Delete /></el-icon>
                    </div>
                  </div>
                  <div v-else class="editor-tip">{{ $t('agentSettings.emptySection') }}</div>
                </div>

                <div class="tools-save-bar">
                  <el-button size="small" type="primary" :loading="savingGrants"
                             :disabled="!agentId || locked" @click="saveGrants">{{ $t('agentSettings.saveGrants') }}</el-button>
                  <span v-if="grantsDirty" class="dirty-tip">{{ $t('agentSettings.dirtyTip') }}</span>
                  <span v-else class="tools-count">{{ $t('agentSettings.savedTip') }}</span>
                </div>
              </div>

              <!-- 常驻摘要：结论与提示一眼可见，点「详情」才进覆盖视图 -->
              <div v-if="effectiveSummary" class="effective-bar">
                <span class="effective-text">{{ effectiveSummary.text }}</span>
                <span v-if="effectiveSummary.warn" class="ref-tag warn">{{ $t('agentSettings.warningsCount', { n: effectiveSummary.warn }) }}</span>
                <span v-if="effectiveSummary.degraded" class="ref-tag warn">{{ $t('agentSettings.degradedBadge') }}</span>
                <el-button size="small" text class="effective-detail" @click="previewEffectiveTools">{{ $t('agentSettings.detail') }}</el-button>
              </div>
              <div v-else-if="agentId" class="editor-tip">{{ $t('agentSettings.effectiveLoading') }}</div>
              </template>
            </template>
          </div>
        </div>
        </teleport>

        <!-- 工具授权：与设置面板同款侧栏，贴在设置面板左侧。
             按「必须导入 / 可使用 / 拒绝」分为三个区，每区各自管理自己的工具与组。
             teleport 到 body：避免被内容区的 overflow:hidden 裁剪（左侧有对话窗口时会显得"被遮挡"） -->
        <teleport to="body">
        <div v-if="grantPickerVisible" ref="pickerPanelRef" class="picker-panel" :style="pickerStyle">
          <div class="panel-header">
            <span class="panel-title">{{ $t('agentSettings.grantsPanelTitle') }}</span>
            <span class="close-icon" @click="grantPickerVisible = false">&#xd7;</span>
          </div>
          <div class="panel-body">
            <!-- 三个区（数量标在标签上） -->
            <div class="picker-tabs">
              <button v-for="k in grantKinds" :key="k" class="picker-tab"
                      :class="{ active: pickerKind === k }" @click="pickerKind = k">
                {{ grantKindLabel(k) }}<span class="picker-tab-count">{{ grantCounts[k] || 0 }}</span>
              </button>
            </div>

            <!-- 添加到本区（已加入的项在右侧设置面板的工具配置里展示） -->
            <div class="picker-block-title">
              {{ $t('agentSettings.addToKind', { kind: grantKindLabel(pickerKind) }) }}
              <span class="picker-block-hint">{{ $t('agentSettings.addedCount', { n: grantCounts[pickerKind] || 0 }) }}</span>
            </div>
            <div class="picker-toolbar">
              <el-input v-model="grantSearch" size="small" :placeholder="$t('agentSettings.searchGrantPlaceholder')" clearable style="width:100%" />
              <el-radio-group v-model="pickerFilterKind" size="small" class="picker-kinds">
                <el-radio-button value="">{{ $t('agentSettings.filterAll') }}</el-radio-button>
                <el-radio-button value="builtin">{{ $t('agentSettings.filterBuiltin') }}</el-radio-button>
                <el-radio-button value="preset">{{ $t('agentSettings.filterPreset') }}</el-radio-button>
                <el-radio-button value="group">{{ $t('agentSettings.filterGroupNames') }}</el-radio-button>
                <el-radio-button value="ext">{{ $t('agentSettings.filterExt') }}</el-radio-button>
                <el-radio-button value="conn">{{ $t('agentSettings.filterConn') }}</el-radio-button>
              </el-radio-group>
            </div>
            <!-- 组卡片：点头部=整组加入（组内工具等同全选中）；点组内某一行=只加这个工具。
                 synthetic（插件 / 自定义工具的类别容器）不可整组授权，只展开成员，不做整卡选中。 -->
            <div class="picker-cards">
              <div v-for="c in pickerCards" :key="c.ref" class="picker-card"
                   :class="{ active: !c.synthetic && inActiveKind(c.ref) }">
                <div class="picker-card-head" @click="c.synthetic ? toggleCollapse(c.ref) : toggleGrantRef(c)">
                  <el-icon class="picker-caret" :class="{ open: !isCollapsed(c.ref) }"
                           @click.stop="toggleCollapse(c.ref)"><CaretRight /></el-icon>
                  <div class="tool-item-main">
                    <div class="tool-name">
                      {{ groupName(t, te, c) }}
                      <span class="ref-tag">{{ c.synthetic ? $t('agentSettings.tagCategory') : $t('agentSettings.tagGroup') }}</span>
                      <span class="ref-tag">{{ $t('agentSettings.countItems', { n: c.count }) }}</span>
                      <!-- 类别卡的名字本身就是类别名（插件 / 自定义工具），不再重复标 kind -->
                      <span v-if="!c.synthetic" class="ref-tag">{{ kindLabel(c.kind) }}</span>
                      <span v-if="!c.synthetic && kindOfRef(c.ref) && !inActiveKind(c.ref)" class="ref-tag warn">{{ $t('agentSettings.alreadyIn', { kind: grantKindLabel(kindOfRef(c.ref)) }) }}</span>
                      <span v-if="c.available === false" class="ref-tag warn"
                            :title="unavailableTitle(c) || $t('agentSettings.unavailableTip')">{{ unavailableLabel(c.unavailable_kind) }}</span>
                    </div>
                    <div class="tool-desc" :title="groupDesc(t, te, c) || c.ref">
                      {{ shortText(groupDesc(t, te, c)) || (c.synthetic ? $t('agentSettings.syntheticHint') : c.ref) }}
                    </div>
                  </div>
                  <el-icon v-if="!c.synthetic" class="picker-check"><Check v-if="inActiveKind(c.ref)" /><Plus v-else /></el-icon>
                </div>
                <div v-if="!isCollapsed(c.ref) && (c.items || []).length" class="picker-card-body">
                  <div v-for="t in c.items" :key="t.ref" class="picker-tool"
                       :class="{ on: inActiveKind(t.ref), covered: isCovered(t.ref) }"
                       @click.stop="toggleTool(t)">
                    <span class="picker-tool-name" :title="t.description || t.ref">{{ t.label }}</span>
                    <span v-if="shortDesc(t)" class="picker-tool-desc">{{ shortDesc(t) }}</span>
                    <span v-if="kindOfRef(t.ref) && !inActiveKind(t.ref)" class="ref-tag warn">{{ $t('agentSettings.alreadyIn', { kind: grantKindLabel(kindOfRef(t.ref)) }) }}</span>
                    <span v-if="isCovered(t.ref)" class="ref-tag">{{ $t('agentSettings.coveredByGroup') }}</span>
                    <span class="picker-tool-mark">{{ inActiveKind(t.ref) || isCovered(t.ref) ? '✓' : '+' }}</span>
                  </div>
                </div>
              </div>
              <div v-if="!pickerCards.length" class="editor-tip">{{ $t('agentSettings.noCandidates') }}</div>
            </div>
          </div>
          <div class="picker-footer">
            <span class="tools-count">{{ $t('agentSettings.pickerSaveHint') }}</span>
            <el-button size="small" type="primary" class="effective-detail"
                       @click="grantPickerVisible = false">{{ $t('agentSettings.done') }}</el-button>
          </div>
        </div>
        </teleport>

        <!-- 技能选择：与「工具授权」同款侧栏（同几何、同卡片样式），贴在设置面板左侧。
             点一项立即绑定 / 解绑并落库；私有 / 废弃技能可见但不可点。 -->
        <teleport to="body">
        <div v-if="skillPickerVisible" ref="skillPickerPanelRef" class="picker-panel" :style="pickerStyle">
          <div class="panel-header">
            <span class="panel-title">{{ $t('agentSettings.skillsPanelTitle') }}</span>
            <span class="close-icon" @click="skillPickerVisible = false">&#xd7;</span>
          </div>
          <div class="panel-body">
            <div class="picker-block-title">
              {{ $t('agentSettings.skillToggleHint') }}
              <span class="picker-block-hint">{{ $t('agentSettings.boundCount', { n: boundSkillIds.length }) }}</span>
            </div>
            <div class="picker-toolbar">
              <el-input v-model="skillSearch" size="small" :placeholder="$t('agentSettings.searchSkillPlaceholder')"
                        clearable style="width:100%" />
            </div>
            <div class="picker-cards">
              <div v-for="s in filteredSkills" :key="s.id" class="picker-card"
                   :class="{ active: isSkillBound(s.id), disabled: !isBindableSkill(s) }">
                <div class="picker-card-head" @click="isBindableSkill(s) && toggleSkillBinding(s.id)">
                  <div class="tool-item-main">
                    <div class="tool-name">
                      {{ s.name }}
                      <span class="ref-tag" :class="skillTagClass(s)">{{ skillTagText(s) }}</span>
                      <span v-if="isSkillBound(s.id)" class="ref-tag core">{{ $t('agentSettings.boundBadge') }}</span>
                    </div>
                    <div class="tool-desc" :title="s.description || $t('agentSettings.noDescription')">{{ s.description || $t('agentSettings.noDescription') }}</div>
                  </div>
                  <el-icon class="picker-check">
                    <Check v-if="isSkillBound(s.id)" /><Plus v-else-if="isBindableSkill(s)" />
                    <Lock v-else />
                  </el-icon>
                </div>
              </div>
              <div v-if="!filteredSkills.length" class="editor-tip">{{ $t('agentSettings.noMatchingSkills') }}</div>
            </div>
          </div>
          <div class="picker-footer">
            <span class="tools-count">{{ $t('agentSettings.savedImmediately') }}</span>
            <el-button size="small" type="primary" @click="skillPickerVisible = false">{{ $t('agentSettings.done') }}</el-button>
          </div>
        </div>
        </teleport>

        <!-- 调用授权：两个方向共用这一个侧栏（标题 / 候选随方向变），点一项即授权 / 收回： -->
        <teleport to="body">
        <div v-if="agentPickerVisible" ref="agentPickerPanelRef" class="picker-panel" :style="pickerStyle">
          <div class="panel-header">
            <span class="panel-title">{{ agentPickerTitle }}</span>
            <span class="close-icon" @click="agentPickerVisible = false">&#xd7;</span>
          </div>
          <div class="panel-body">
            <div class="picker-block-title">
              {{ $t('agentSettings.agentToggleHint') }}
              <span class="picker-block-hint">{{ $t('agentSettings.grantedCount', { n: currentDirIds.length }) }}</span>
            </div>
            <div class="picker-toolbar">
              <el-input v-model="agentSearch" size="small" :placeholder="$t('agentSettings.searchAgentPlaceholder')"
                        clearable style="width:100%" />
            </div>
            <div class="picker-cards">
              <div v-for="a in filteredPickerAgents" :key="a.id" class="picker-card"
                   :class="{ active: isAgentBound(a.id) }">
                <div class="picker-card-head" @click="toggleAgentCall(a.id)">
                  <div class="tool-item-main">
                    <div class="tool-name">
                      {{ a.name }}
                      <span v-if="a.is_supervisor" class="ref-tag core">supervisor</span>
                      <span v-if="isAgentUnreleased(a.id)" class="ref-tag warn" :title="unreleasedTip">{{ $t('agentSettings.unreleasedBadge') }}</span>
                      <span v-if="isAgentBound(a.id)" class="ref-tag core">{{ $t('agentSettings.grantedBadge') }}</span>
                    </div>
                    <div class="tool-desc" :title="a.description || $t('agentSettings.noDescription')">{{ a.description || $t('agentSettings.noDescription') }}</div>
                  </div>
                  <el-icon class="picker-check">
                    <Check v-if="isAgentBound(a.id)" /><Plus v-else />
                  </el-icon>
                </div>
              </div>
              <div v-if="!filteredPickerAgents.length" class="editor-tip">{{ $t('agentSettings.noMatchingAgents') }}</div>
            </div>
          </div>
          <div class="picker-footer">
            <span class="tools-count">{{ $t('agentSettings.savedImmediately') }}</span>
            <el-button size="small" type="primary" @click="agentPickerVisible = false">{{ $t('agentSettings.done') }}</el-button>
          </div>
        </div>
        </teleport>
</template>

<script setup>
import { ref, computed, watch, nextTick, onUnmounted } from 'vue'
import { useI18n } from 'vue-i18n'
import { ElMessage, ElMessageBox } from 'element-plus'
import { Folder, Tools, Delete } from '@element-plus/icons-vue'
import adminApi from '../../config/admin.js'
import { unavailableText } from '../../utils/unavailable'
import { groupName, groupDesc } from '../../utils/groupText.js'
import { getToken } from '../../utils/auth.js'
import { useEscClose } from '../../composables/useEscClose'
import { formatError } from '../../../locales'

const props = defineProps({
  visible: { type: Boolean, default: false },
  // 定位基准：编辑器的内容容器（面板贴它的右边缘、与它同高）
  anchorEl: { type: Object, default: null },
  agentId: { type: String, default: '' },
  // 编辑中的 agent 表单（对象由父组件持有，这里只就地改字段）
  agent: { type: Object, default: () => ({}) },
  // 只读（状态为「废弃」）
  locked: { type: Boolean, default: false },
  // 超级权限身份（授予/撤销由本组件发起，父组件据事件回写）
  supervisor: { type: Boolean, default: false },
  // 全量 agent 列表（调用授权里挑「谁能调用它 / 它能调用谁」）
  agents: { type: Array, default: () => [] },
  // 父组件手动刷新时自增，用来让本组件把关系/授权/生效信息重读一遍
  refreshToken: { type: Number, default: 0 }
})
const emit = defineEmits(['close', 'supervisor-change', 'refresh-list'])

// 脚本侧文案（标签映射、toast、摘要）同样走 t()：切语言后模板重渲染时才会跟着变
const { t, te } = useI18n()

const authHeaders = () => ({
  'Content-Type': 'application/json',
  'Authorization': `Bearer ${getToken()}`
})

// 调用授权可选项：排除自己（不允许自己调用自己）
const selectableAgents = computed(() =>
  props.agents.filter(a => String(a.id) !== String(props.agentId))
)

// 调用授权侧栏：两个方向共用同一个面板，方向决定标题、候选名单与写入的字段。
// supervisor 被调起时仍是完整的管理面（恒读草稿），能不能调完全由这里写下的授权行决定。
const agentPickerVisible = ref(false)
const agentPickerDirection = ref('in') // 'in' 谁能调用它 / 'out' 它能调用谁
const agentSearch = ref('')
const agentPickerPanelRef = ref(null)
const agentPickerTitle = computed(() => t(agentPickerDirection.value === 'in'
  ? 'agentSettings.callerPanelTitle' : 'agentSettings.calleePanelTitle'))
// 当前方向的已授权 id：模板里的 tag 与面板里的勾选状态共用这一份
const currentDirIds = computed(() =>
  (agentPickerDirection.value === 'in' ? props.agent.called_by_ids : props.agent.can_call_ids) || []
)

// 技能绑定（agent_skill_bindings 的 agent 侧）：全部技能都列出来，可绑定的是租户级未废弃的
const allSkills = ref([])        // 全部技能（租户级 + 各 agent 私有）
const boundSkillIds = ref([])    // 已绑定

// 设置面板几何：teleport 到 body 后用 fixed 定位，避免被内容容器 overflow:hidden 裁剪
const settingsRect = ref({ top: 0, left: 0, width: 420, height: 600 })
const settingsPanelStyle = computed(() => ({
  top: settingsRect.value.top + 'px',
  left: settingsRect.value.left + 'px',
  width: settingsRect.value.width + 'px',
  height: settingsRect.value.height + 'px'
}))
const updateSettingsRect = () => {
  const el = props.anchorEl
  if (!el) return
  const r = el.getBoundingClientRect()
  const w = settingsPanelWidth.value
  settingsRect.value = {
    top: r.top,
    left: Math.max(0, r.right - w), // 容器比面板窄时贴窗口左边缘，保证整块可见（已脱离容器裁剪）
    width: w,
    height: r.height
  }
}
const settingsPanelRef = ref(null)

// 「添加工具 / 组」侧栏（与设置面板同款，贴在设置面板左侧，宽度跟随设置面板）
const pickerPanelRef = ref(null)
// 「技能选择」侧栏：与工具授权同一几何（pickerStyle），两个面板互斥打开
const skillPickerPanelRef = ref(null)
// 侧栏几何：teleport 到 body 后用 fixed 定位，坐标按设置面板的实际位置实时计算
const pickerRect = ref({ top: 0, left: 0, width: 420, height: 600 })
const pickerStyle = computed(() => ({
  top: pickerRect.value.top + 'px',
  left: pickerRect.value.left + 'px',
  width: pickerRect.value.width + 'px',
  height: pickerRect.value.height + 'px'
}))
const updatePickerRect = () => {
  const el = settingsPanelRef.value
  if (!el) return
  const r = el.getBoundingClientRect()
  // 左侧空间不足时收缩宽度（下限 280），避免压住设置面板；再不够才贴窗口左边缘
  const width = Math.min(r.width, Math.max(280, r.left))
  pickerRect.value = {
    top: r.top,
    left: Math.max(0, r.left - width),
    width,
    height: r.height
  }
}
// 设置面板宽度（可拖拽，对齐工作流试运行面板交互）
const settingsPanelWidth = ref(420)

// 拖拽改宽：向左拖变宽（320~720）
const startSettingsResize = (e) => {
  const startX = e.clientX
  const startWidth = settingsPanelWidth.value
  const onMouseMove = (ev) => {
    const deltaX = startX - ev.clientX
    settingsPanelWidth.value = Math.max(320, Math.min(startWidth + deltaX, 720))
  }
  const onMouseUp = () => {
    document.removeEventListener('mousemove', onMouseMove)
    document.removeEventListener('mouseup', onMouseUp)
  }
  document.addEventListener('mousemove', onMouseMove)
  document.addEventListener('mouseup', onMouseUp)
}

// 点击设置面板外部时自动关闭
const handleSettingsPanelOutsideClick = (e) => {
  const target = e.target
  // 忽略 Element Plus 浮层（下拉/提示等 teleport 到 body 的元素）的点击
  if (target.closest && (target.closest('.el-overlay') || target.closest('.el-select-dropdown') || target.closest('.el-popper'))) {
    return
  }
  // 点「设置」按钮本身交给按钮的 click（toggle 逻辑），这里不处理
  if (target.closest && target.closest('.settings-toggle-btn')) {
    return
  }
  // 点左侧「工具授权」/「技能选择」侧栏不算外部点击（它们是设置面板的兄弟面板）
  if (pickerPanelRef.value && pickerPanelRef.value.contains(target)) {
    return
  }
  if (skillPickerPanelRef.value && skillPickerPanelRef.value.contains(target)) {
    return
  }
  // 「谁能调用它 / 它能调用谁」的选择侧栏同理
  if (agentPickerPanelRef.value && agentPickerPanelRef.value.contains(target)) {
    return
  }
  if (settingsPanelRef.value && !settingsPanelRef.value.contains(target)) {
    emit('close')
  }
}
// 面板开关：
//  - 打开时按编辑区定位并挂外部点击监听，回到「调用授权」并重读关系与技能绑定（数据懒加载）
//  - 关闭时收掉左侧「工具授权」侧栏
watch(() => props.visible, async (visible) => {
  if (visible) {
    // nextTick 延迟绑定，避免打开按钮的 mousedown 立即触发关闭；同时按编辑区实际位置定位
    nextTick(() => {
      document.addEventListener('mousedown', handleSettingsPanelOutsideClick)
      updateSettingsRect()
    })
    window.addEventListener('resize', updateSettingsRect)
    settingsTab.value = 'relations'
    previewVisible.value = false
    if (props.agentId) await Promise.all([loadRelations(), loadSkillBindings()])
  } else {
    document.removeEventListener('mousedown', handleSettingsPanelOutsideClick)
    window.removeEventListener('resize', updateSettingsRect)
    grantPickerVisible.value = false // 设置面板关闭时，一并关掉左侧的侧栏
    skillPickerVisible.value = false
    agentPickerVisible.value = false
  }
})
// 拖拽改宽：重新锚定面板位置（并让左侧侧栏跟随）
watch(settingsPanelWidth, () => {
  if (!props.visible) return
  nextTick(() => {
    updateSettingsRect()
    if (grantPickerVisible.value || skillPickerVisible.value || agentPickerVisible.value) updatePickerRect()
  })
})
onUnmounted(() => {
  document.removeEventListener('mousedown', handleSettingsPanelOutsideClick)
  window.removeEventListener('resize', updatePickerRect)
  window.removeEventListener('resize', updateSettingsRect)
})
// 设置面板 tab：relations（调用授权）| tools（工具配置）
const settingsTab = ref('relations')
// ===== 工具授权（工具授权体系） =====
const toolCatalog = ref([])     // 可引用组目录（含展开成员）
let toolCatalogAgentId = null   // 目录对应的 agent 视角（换 agent / 进编辑器时失效）
const toolsLoading = ref(false)
const grantsLoaded = ref(false)
const grantsAgentId = ref('')   // 已加载授权的 agent id（切换 agent 时重新拉取）
const savingGrants = ref(false)
const grantsDirty = ref(false)
// 草稿：{ unrestricted, items: [{ ref, item_type, label, kind, count, available, grant_kind }] }
const grantDraft = ref({ unrestricted: false, items: [] })
const grantPickerVisible = ref(false)
// 技能选择侧栏（与工具授权互斥，同一套几何与卡片样式）
const skillPickerVisible = ref(false)
const skillSearch = ref('')
const pickerFilterKind = ref('')
const pickerKind = ref('required') // 当前管理的区：required（必须导入）/ allow（可使用）/ deny（拒绝）
const grantSearch = ref('')
const previewVisible = ref(false)
const previewData = ref(null)

// ESC 关闭：工具授权侧栏 / 生效预览这些内层视图开着时先退它们，再按一次才退设置面板
// （栈顶优先，见 useEscClose）
useEscClose(() => props.visible, () => emit('close'))
useEscClose(grantPickerVisible, () => { grantPickerVisible.value = false })
useEscClose(skillPickerVisible, () => { skillPickerVisible.value = false })
useEscClose(agentPickerVisible, () => { agentPickerVisible.value = false })
useEscClose(previewVisible, () => { previewVisible.value = false })
// 进入编辑器时清掉「按 agent 缓存」的工具面状态。详情本身每次都重拉，但授权草稿与可引用目录
// 会按 agentId 命中缓存（见 loadToolGrants / loadToolCatalog 的短路），外部（管理端、超级智能体
// agent 自己）改过之后重开同一张卡片仍会显示旧值 —— 清空后这两处按需重新拉取。
const resetToolGrantCache = () => {
  grantsLoaded.value = false
  grantsAgentId.value = ''
  grantsDirty.value = false
  grantDraft.value = { unrestricted: false, items: [] }
  grantPickerVisible.value = false
  skillPickerVisible.value = false
  agentPickerVisible.value = false
  previewVisible.value = false
  previewData.value = null
  toolCatalogAgentId = null
}

// 调用授权（双向）
const loadRelations = async (id = props.agentId) => {
  if (!id) return
  try {
    const res = await fetch(adminApi.agents.relations(id), { headers: authHeaders() })
    if (!res.ok) return
    const data = await res.json()
    const selfId = String(id)
    // 过滤自身：不允许自己调用自己（历史数据可能残留自引用）
    props.agent.can_call_ids = (data?.data?.can_call || [])
      .map((x) => x.callee_id).filter((x) => String(x) !== selfId)
    props.agent.called_by_ids = (data?.data?.called_by || [])
      .map((x) => x.caller_id).filter((x) => String(x) !== selfId)
    // supervisor 可被调用：入边照常展示，历史授权不再丢弃（后果由「恒读草稿」兜住）
  } catch { /* 忽略：取不到时按未授权处理 */ }
}

// tag 里显示名字：agent 被删后 id 还在库里时退回显示 id，至少不空白
const agentName = (id) => props.agents.find((a) => String(a.id) === String(id))?.name || id
// 未发布正式版本：授权能建（常见路径是「先建 agent + 授权，再发布」），但正式链
// （MCP / RPC / 端点 / flow）调用会报「尚未发布正式版本」，只有编辑页调试能调起来。
// 列表接口的 current_version 为 0 即未发布；字段缺失（非列表来源）按已发布处理，避免误报。
const unreleasedTip = computed(() => t('agentSettings.unreleasedTip'))
const isAgentUnreleased = (id) => {
  const a = props.agents.find((x) => String(x.id) === String(id))
  return !!a && a.current_version === 0
}
const isAgentBound = (id) => currentDirIds.value.some((x) => String(x) === String(id))

// 候选：全量 agent 减去自己（不允许自己调用自己），两个方向同一份名单，都允许包含 supervisor
const filteredPickerAgents = computed(() => {
  const q = agentSearch.value.trim().toLowerCase()
  if (!q) return selectableAgents.value
  return selectableAgents.value.filter((a) =>
    String(a.name || '').toLowerCase().includes(q) ||
    String(a.description || '').toLowerCase().includes(q))
})

// 打开选择侧栏：两个方向共用，三个侧栏互斥
const openAgentPicker = async (dir) => {
  if (!props.agentId || props.locked) return
  agentPickerDirection.value = dir === 'out' ? 'out' : 'in'
  agentSearch.value = ''
  grantPickerVisible.value = false
  skillPickerVisible.value = false
  agentPickerVisible.value = true
  await nextTick()
  updatePickerRect() // 按设置面板当前位置定位
}

// 点一项：已授权就收回，未授权就授权；立即保存（与技能绑定同款）
const toggleAgentCall = async (id) => {
  if (props.locked) return
  const key = agentPickerDirection.value === 'in' ? 'called_by_ids' : 'can_call_ids'
  const cur = props.agent[key] || []
  if (cur.some((x) => String(x) === String(id))) {
    props.agent[key] = cur.filter((x) => String(x) !== String(id))
  } else {
    props.agent[key] = [...cur, id]
  }
  await saveRelations()
}

// 关掉 tag = 收回这一条授权
const unbindAgentCall = async (dir, id) => {
  if (props.locked) return
  const key = dir === 'in' ? 'called_by_ids' : 'can_call_ids'
  props.agent[key] = (props.agent[key] || []).filter((x) => String(x) !== String(id))
  await saveRelations()
}

// ===== 工具授权（工具授权体系）：目录 / 草稿 / 保存 / 预览 =====

const KIND_LABEL_KEYS = {
  builtin: 'kindBuiltin', mcp: 'kindMcp', rpc: 'kindRpc', local: 'kindLocal',
  group: 'kindGroup', preset: 'kindPreset', plugin: 'kindPlugin', custom: 'kindCustom'
}
const kindLabel = (k) => (KIND_LABEL_KEYS[k] ? t(`agentSettings.${KIND_LABEL_KEYS[k]}`) : k)

// 拉取可引用目录：含天然组 / 预设组 / 自定义组 / 连接（含展开成员）。
// 按 agent 视角拉取（非超级智能体会隐藏「仅超级智能体可用」的管理类工具），
// 切换 agent 时按 agentId 重新拉取。
const loadToolCatalog = async (agentId = '') => {
  if (toolCatalog.value.length && toolCatalogAgentId === agentId) return
  try {
    const res = await fetch(adminApi.toolCatalog.list(agentId), { headers: authHeaders() })
    const body = await res.json().catch(() => ({}))
    if (body.code === 200) {
      toolCatalog.value = body.data?.groups || []
      toolCatalogAgentId = agentId
    } else {
      ElMessage.warning(formatError(body) || t('agentSettings.catalogLoadFailed'))
    }
  } catch (e) {
    console.error('failed to load the tool catalog:', e)
  }
}

// 一句话简述：最多 20 个字（超出截断加省略号，完整内容放 title）
const shortText = (text) => {
  const s = String(text || '').trim()
  if (!s) return ''
  return s.length > 20 ? s.slice(0, 19) + '…' : s
}
const shortDesc = (item) => shortText(item?.description)

// 不可用徽标文案按归因分开：用户据此知道该找平台还是找自己
// （kind 取值见后端 toolgrant.Unavailable* 常量；不认识的新值退化成「不可用」而不是空白）
const UNAVAILABLE_LABEL_KEYS = {
  capability: 'unavailableCapability',
  plugin_policy: 'unavailablePluginPolicy',
  plugin_inactive: 'unavailablePluginInactive',
  connection: 'unavailableConnection',
  endpoint: 'unavailableEndpoint'
}
const unavailableLabel = (kind) => t(`agentSettings.${UNAVAILABLE_LABEL_KEYS[kind] || 'unavailableGeneric'}`)

// 不可用原因（可翻译）：徽标只说「归到哪一层」，具体问题在原因里。
// 后端给了 unavailable_code 就本地化，没有（旧后端）才显示它那句中文。
const unavailableTitle = (item) => unavailableText(t, te, item, '')

// 原因三件套要一起搬：只搬 kind / reason 会让 tooltip 在英文界面里退回中文，
// 所以下面所有「从目录拷一份展示信息」的地方都用这个展开，别再逐字段手写。
const unavailableFields = (item) => ({
  unavailable_reason: item?.unavailable_reason || '',
  unavailable_kind: item?.unavailable_kind || '',
  unavailable_code: item?.unavailable_code || '',
  unavailable_params: item?.unavailable_params || null
})

// 按 ref 从目录里找到展示信息（label / 工具数 / 可用性 / 简述）
const describeRef = (ref, itemType) => {
  for (const g of toolCatalog.value) {
    if (itemType !== 'tool' && g.ref === ref) {
      return { ref, item_type: 'group', label: g.name, kind: g.kind, available: g.available,
               count: (g.items || []).length, description: g.description || '',
               ...unavailableFields(g) }
    }
    for (const t of (g.items || [])) {
      if (t.ref === ref) {
        // 命中成员就是**工具**：历史数据里插件 / 自定义工具曾被记成 item_type=group
        // （一个工具一张伪组卡片），显示时按真相纠正，下次保存会写成 tool。
        return { ref, item_type: 'tool', label: t.label || t.name,
                 kind: g.kind, available: t.available, count: 0, description: t.description || '',
                 ...unavailableFields(t) }
      }
    }
  }
  return { ref, item_type: itemType || 'tool', label: ref, kind: '', available: null, count: 0, description: '',
           ...unavailableFields(null) }
}

// 读取该 agent 的授权项与「不限制工具」开关
const loadToolGrants = async () => {
  if (!props.agentId) {
    grantDraft.value = { unrestricted: false, items: [] }
    grantsDirty.value = false
    previewData.value = null
    previewVisible.value = false
    return
  }
  if (grantsLoaded.value && grantsAgentId.value === props.agentId) return
  // 切换/重新加载 agent：回到配置视图并清掉上一份生效信息
  previewVisible.value = false
  previewData.value = null
  toolsLoading.value = true
  try {
    await loadToolCatalog(props.agentId)
    const res = await fetch(adminApi.agents.toolGrants(props.agentId), { headers: authHeaders() })
    const body = await res.json().catch(() => ({}))
    if (body.code !== 200) throw new Error(formatError(body) || t('agentSettings.grantsLoadFailed'))
    const d = body.data || {}
    grantDraft.value = {
      unrestricted: !!d.unrestricted,
      items: (d.grants || []).map((g) => ({ ...describeRef(g.ref, g.item_type), grant_kind: g.grant_kind }))
    }
    grantsLoaded.value = true
    grantsAgentId.value = props.agentId
    grantsDirty.value = false
  } catch (e) {
    ElMessage.error(e.message || t('agentSettings.grantsLoadFailed'))
  } finally {
    toolsLoading.value = false
  }
}

const grantCounts = computed(() => {
  const c = { required: 0, allow: 0, deny: 0 }
  for (const i of grantDraft.value.items) if (c[i.grant_kind] !== undefined) c[i.grant_kind]++
  return c
})

// 配置视图的分段：按 kind 分三组，每组内「组在前、工具在后」，同类再按名称排序
const grantsByKind = computed(() => {
  const out = { required: [], allow: [], deny: [] }
  for (const i of grantDraft.value.items) {
    if (out[i.grant_kind]) out[i.grant_kind].push(i)
  }
  for (const k of Object.keys(out)) {
    out[k].sort((a, b) => {
      if (a.item_type !== b.item_type) return a.item_type === 'group' ? -1 : 1
      return String(a.label || '').localeCompare(String(b.label || ''), 'zh')
    })
  }
  return out
})

// 三个区（tab）
const grantKinds = ['required', 'allow', 'deny']
const GRANT_KIND_KEYS = { required: 'grantKindRequired', allow: 'grantKindAllow', deny: 'grantKindDeny' }
const grantKindLabel = (k) => (GRANT_KIND_KEYS[k] ? t(`agentSettings.${GRANT_KIND_KEYS[k]}`) : k)
// 本区已加入的项
const activeGrants = computed(() => grantDraft.value.items.filter((i) => i.grant_kind === pickerKind.value))
// 某个 ref（组或工具）当前落在哪个区（'' = 未加入）
const kindOfRef = (ref) => {
  const hit = grantDraft.value.items.find((i) => i.ref === ref)
  return hit ? hit.grant_kind : ''
}
const inActiveKind = (ref) => kindOfRef(ref) === pickerKind.value
// 本区已加入的组覆盖的成员工具（组被选走 = 组内工具都算选中）
const activeGroupMembers = computed(() => {
  const s = new Set()
  for (const it of activeGrants.value) {
    if (it.item_type !== 'group') continue
    const g = toolCatalog.value.find((x) => x.ref === it.ref)
    for (const t of (g?.items || [])) s.add(t.ref)
  }
  return s
})
const isCovered = (ref) => activeGroupMembers.value.has(ref)

// 候选：按「组卡片 + 组内工具」组织（组可整组加入，工具可单个加入）
const pickerCards = computed(() => {
  const kw = grantSearch.value.trim().toLowerCase()
  const hit = (text) => (text || '').toLowerCase().includes(kw)
  const cards = []
  for (const g of toolCatalog.value) {
    // 内置工具对**所有** agent 一视同仁地可选。
    // 早期对超级智能体隐藏（「内置是固有基线，勾了也没意义」）—— 那套基线已经取消：
    // 超级智能体的常驻集现在是「必须导入」里的 builtin:core 行，所以这里必须能看见、能增删，
    // 否则「把 browser 加给超级智能体」「把 shell 从超级智能体拿掉」都没有入口。
    // 插件与自定义工具单独成类：混进「连接/端点」会让人以为要去设置页配连接
    const bucket = ['builtin', 'preset', 'group'].includes(g.kind)
      ? g.kind
      : (['plugin', 'script'].includes(g.kind) ? 'ext' : 'conn')
    if (pickerFilterKind.value && bucket !== pickerFilterKind.value) continue
    const items = (g.items || []).map((t) => ({
      ref: t.ref, item_type: 'tool', label: t.label || t.name, kind: g.kind,
      available: t.available, description: t.description || '',
      // 不可用原因原样透传（平台未开通 / 租户未启用 / 连接未启用 要能分开看）
      ...unavailableFields(t)
    }))
    // 译名也要能搜到：英文界面下搜 built-in 应该命中「Built-in · file」
    const groupHit = !kw || hit(groupName(t, te, g)) || hit(g.name) || hit(g.ref)
    const matched = groupHit ? items : items.filter((t) => hit(t.label) || hit(t.ref))
    if (kw && !groupHit && !matched.length) continue
    cards.push({
      ref: g.ref, item_type: 'group', label: g.name, kind: g.kind,
      available: g.available, count: (g.items || []).length,
      description: g.description || '', items,
      // 类别容器（插件 / 自定义工具）：不可整组授权，勾选落到成员的工具 ref 上
      synthetic: !!g.synthetic,
      ...unavailableFields(g)
    })
  }
  return cards
})

const openGrantPicker = async () => {
  await loadToolCatalog(props.agentId)
  grantSearch.value = ''
  pickerFilterKind.value = ''
  pickerKind.value = 'required' // 每次打开都回到第一区，与标签顺序一致
  grantPickerVisible.value = true
  await nextTick()
  updatePickerRect() // 按设置面板当前位置定位
}

// 切换分区时若筛选器选中的类别在该区没有候选（例如只有插件/脚本），保持原位即可 ——
// 内置类对超级智能体不再特殊，无需在切区时重置筛选器。

// 侧栏打开期间：窗口尺寸变化时重算位置；两个侧栏都关了才移除监听
const anyPickerVisible = computed(() =>
  grantPickerVisible.value || skillPickerVisible.value || agentPickerVisible.value)
watch(anyPickerVisible, (visible) => {
  if (visible) {
    nextTick(updatePickerRect)
    window.addEventListener('resize', updatePickerRect)
  } else {
    window.removeEventListener('resize', updatePickerRect)
  }
})

// 把 ref 加入本区；加「组」时，同区的同组成员工具会被去掉（组已覆盖它们，避免重复）。
// 注意只清理"同区"：例如「拒绝」里单独拒绝的成员工具，会在「可使用」加组后保留（作为例外）。
const addGrantToKind = (opt) => {
  const kind = pickerKind.value
  if (opt.item_type === 'group') {
    const members = new Set((toolCatalog.value.find((g) => g.ref === opt.ref)?.items || []).map((t) => t.ref))
    grantDraft.value.items = grantDraft.value.items.filter(
      (i) => !(i.grant_kind === kind && i.item_type === 'tool' && members.has(i.ref))
    )
  }
  const exist = grantDraft.value.items.find((i) => i.ref === opt.ref)
  if (exist) exist.grant_kind = kind
  else {
    // 只保留展示与提交需要的字段（卡片对象里还带着 items 数组，不要带进草稿）
    grantDraft.value.items.push({
      ref: opt.ref, item_type: opt.item_type, grant_kind: kind,
      label: opt.label, kind: opt.kind, count: opt.count || 0,
      available: opt.available, description: opt.description || '',
      ...unavailableFields(opt)
    })
  }
  grantsDirty.value = true
}

// 切换（组/工具）在本区的加入状态：已在本区 → 移除；不在 / 在别的区 → 加入（等于移过来）
const toggleGrantRef = (opt) => {
  const exist = grantDraft.value.items.find((i) => i.ref === opt.ref)
  if (exist && exist.grant_kind === pickerKind.value) {
    grantDraft.value.items = grantDraft.value.items.filter((i) => i.ref !== opt.ref)
    grantsDirty.value = true
  } else {
    addGrantToKind(opt)
  }
}

// 组内单个工具：已被本区的组覆盖时不可再单独加入
const toggleTool = (tool) => {
  if (isCovered(tool.ref)) return
  toggleGrantRef(tool)
}

// 组卡片收起/展开：缺省收起（只显示组标题），点箭头展开看组内工具。
// 例外：类别容器（插件 / 自定义工具，synthetic）缺省**展开** —— 它的成员就是全部内容，
// 收起后卡片看上去是空的；展开后仍可正常收起（用户手动收过的以用户为准）。
const collapsedGroups = ref({})
const isSyntheticRef = (ref) => toolCatalog.value.some((g) => g.synthetic && g.ref === ref)
const isCollapsed = (ref) => collapsedGroups.value[ref] ?? !isSyntheticRef(ref)
const toggleCollapse = (ref) => {
  collapsedGroups.value = { ...collapsedGroups.value, [ref]: !isCollapsed(ref) }
}

// 超级智能体的常驻组（core）：与后端 shared/models/models.go 的 SupervisorCoreGrantRefs 一致。
// 这里只用于「标 core 徽标 + 删除时二次确认」——它是防误删的提示，不是权限闸
// （真正的判据在后端，删了这一行超级智能体就真的少一整类工具）。
const MAIN_CORE_REFS = [
  'builtin:file', 'builtin:exec', 'builtin:shell',
  'builtin:agent', 'builtin:skill', 'builtin:agent-admin'
]
const isMainCoreRef = (ref) => props.supervisor && MAIN_CORE_REFS.includes(ref)

const removeGrant = async (ref) => {
  if (props.locked) return // 废弃：整屏只读，不动草稿
  // core 组删掉 = 从超级智能体手里收回一整类能力（不报错、不提示，只是静默变少），
  // 误点代价高，所以只有这一类要二次确认；按需组 / 单工具直接删。
  if (isMainCoreRef(ref)) {
    const g = toolCatalog.value.find((x) => x.ref === ref)
    const label = g ? (groupName(t, te, g) || ref) : ref
    try {
      await ElMessageBox.confirm(
        t('agentSettings.confirmRevokeCoreBody', { name: label }),
        t('agentSettings.confirmRevokeCoreTitle'),
        { type: 'warning', confirmButtonText: t('agentSettings.confirmRevoke'), cancelButtonText: t('agentSettings.cancel') }
      )
    } catch { return }
  }
  grantDraft.value.items = grantDraft.value.items.filter((i) => i.ref !== ref)
  grantsDirty.value = true
}

// 未配置时的一键草案：内置全部类「可使用」，等价于旧的「默认全开」。
// 普通能力组全带上；skill-own（自建私有技能）也带上 —— 它默认不授，但属于「普通能力」。
// 四个 *-admin 组对非超级智能体不会出现在目录里（后端按视角剔除），所以不必点名。
// 平台没开通的组仍会被勾上，但会显示「平台未开通」——授权是意图，开通是另一层。
const importCurrentTools = () => {
  const preset = ['builtin:file', 'builtin:exec', 'builtin:shell',
                  'builtin:agent', 'builtin:skill', 'builtin:skill-own']
  const items = []
  for (const ref of preset) {
    const g = toolCatalog.value.find((x) => x.ref === ref)
    if (!g) continue
    items.push({ ref, item_type: 'group', label: g.name, kind: g.kind, available: g.available,
                 count: (g.items || []).length, description: g.description || '', grant_kind: 'allow',
                 ...unavailableFields(g) })
  }
  if (!items.length) { ElMessage.warning(t('agentSettings.presetMissing')); return }
  grantDraft.value.items = items
  grantsDirty.value = true
}

// 保存（全量替换）：显式点保存，避免中间态触发后端的语义校验
const saveGrants = async () => {
  if (!props.agentId) return
  savingGrants.value = true
  try {
    const res = await fetch(adminApi.agents.toolGrants(props.agentId), {
      method: 'PUT',
      headers: authHeaders(),
      body: JSON.stringify({
        unrestricted: grantDraft.value.unrestricted,
        // 只提交 ref：item_type 由服务端按 ref 形态推导（本地那份只用于展示图标/排序）
        grants: grantDraft.value.items.map((i) => ({ grant_kind: i.grant_kind, ref: i.ref }))
      })
    })
    const body = await res.json().catch(() => ({}))
    if (body.code !== 200) throw new Error(formatError(body) || t('agentSettings.saveFailed'))
    grantsDirty.value = false
    // 授权成功 ≠ 现在就能用：平台闸门在授权之上，别让 toast 传达错的承诺。
    // 只数平台侧（能力 / 插件授权）—— 租户自己那层（未启用插件、连接停用）用户当场就能修。
    const platformBlocked = grantDraft.value.items.filter(
      (i) => i.grant_kind !== 'deny' && i.unavailable_kind
        && (i.unavailable_kind === 'capability' || i.unavailable_kind === 'plugin_policy')
    ).length
    if (platformBlocked) {
      ElMessage.success(t('agentSettings.grantsSavedPlatformBlocked', { n: platformBlocked }))
    } else {
      ElMessage.success(t('agentSettings.grantsSaved'))
    }
    await loadEffectiveInfo() // 保存后刷新常驻摘要
  } catch (e) {
    ElMessage.error(e.message || t('agentSettings.saveFailed'))
  } finally {
    savingGrants.value = false
  }
}

// 「不限制工具」开关改动：统一记脏标，与授权项一起由「保存」提交。
// （内置超级智能体现在同样有授权列表与保存栏，不再需要「开关单独即时落库」那条分叉 ——
//  那会让开关与未保存的授权项各写一次，语义上互相覆盖。）
const onUnrestrictedChange = () => {
  grantsDirty.value = true
}

// 外部工具（MCP / RPC / 本地端点）：授权体系已支持，但运行时还没有调用通道，暂不绑定给模型
const isExternalTool = (name) => /^(mcp__|rpc__|local__)/.test(name || '')

// 生效详情：把最终工具按「来源组」归并（组标题 + 组内工具），组名取自工具目录
const previewGroups = computed(() => {
  const map = new Map()
  for (const t of (previewData.value?.final_tools || [])) {
    const ref = t.source || 'other'
    if (!map.has(ref)) {
      const g = toolCatalog.value.find((x) => x.ref === ref)
      map.set(ref, { ref, name: g ? g.name : ref, items: [] })
    }
    map.get(ref).items.push(t)
  }
  return [...map.values()]
})

// 生效详情的组收起/展开（缺省展开）
const collapsedPreviewGroups = ref({})
const isPreviewCollapsed = (ref) => !!collapsedPreviewGroups.value[ref]
const togglePreviewCollapse = (ref) => {
  collapsedPreviewGroups.value = { ...collapsedPreviewGroups.value, [ref]: !isPreviewCollapsed(ref) }
}

// 拉取「生效信息」（打开工具配置 / 保存后刷新）：常驻摘要与详情视图共用
const loadEffectiveInfo = async () => {
  if (!props.agentId) { previewData.value = null; return }
  try {
    const res = await fetch(adminApi.agents.effectiveTools(props.agentId), { headers: authHeaders() })
    const body = await res.json().catch(() => ({}))
    if (body.code === 200) previewData.value = body.data || null
  } catch (e) {
    console.error('failed to load the effective tools:', e)
  }
}

// 常驻摘要：结论 + 提示条数一眼可见，点「详情」才进覆盖视图
const effectiveSummary = computed(() => {
  const d = previewData.value
  if (!d) return null
  if (d.builtin_default) {
    // 内置工具是固有基线，授权项只是「额外追加」—— 因此不能只看 final_tools 的数量说成"缺省"
    const n = (d.final_tools || []).length
    return {
      text: t('agentSettings.summarySupervisor', {
        unrestricted: d.unrestricted ? t('agentSettings.unrestrictedSuffix') : '',
        n
      }),
      warn: (d.warnings || []).length,
      degraded: !!d.degraded
    }
  }
  if (d.no_grants) return { text: t('agentSettings.summaryNoGrants'), warn: 0, degraded: false }
  return {
    text: t('agentSettings.summaryEffective', { n: (d.final_tools || []).length }),
    warn: (d.warnings || []).length,
    degraded: !!d.degraded
  }
})

// 打开详情视图（在面板内覆盖配置内容）
const previewEffectiveTools = async () => {
  if (!props.agentId) return
  previewVisible.value = true
  await loadEffectiveInfo()
}

const switchSettingsTab = async (tab) => {
  settingsTab.value = tab
  if (tab === 'tools') {
    await loadToolGrants()
    await loadEffectiveInfo()
  } else {
    previewVisible.value = false
    // 三个左侧侧栏都是 body 级浮层：切 tab 必须收起来，否则会悬空压住另一侧的内容
    grantPickerVisible.value = false
    skillPickerVisible.value = false
    agentPickerVisible.value = false
  }
}


// 调用授权即时保存（改动即保存）
const saveRelations = async () => {
  if (!props.agentId) return // 未创建：等创建时统一落库
  const f = props.agent
  try {
    const res = await fetch(adminApi.agents.relations(props.agentId), {
      method: 'PUT',
      headers: authHeaders(),
      body: JSON.stringify({ can_call_ids: f.can_call_ids, called_by_ids: f.called_by_ids })
    })
    const body = await res.json().catch(() => ({}))
    if (body.code !== 200) throw new Error(formatError(body) || t('agentSettings.relationsSaveFailed'))
  } catch (e) {
    ElMessage.error(e.message || t('agentSettings.relationsSaveFailed'))
    // 落库失败 → 回到服务端的真实状态，别让 tag 与服务端漂移
    await loadRelations()
  }
}

// 技能标签：能绑定就标可见性，不能绑定就说清原因（本智能体私有 / 归属谁 / 已废弃）。
// 列表全量展示，标签负责区分「看得见」与「绑得上」。
const isBindableSkill = (s) => !s.agent_id && s.status !== 3
const skillTagText = (s) => {
  if (s.status === 3) return t('agentSettings.skillTagDeprecated')
  if (s.agent_id) {
    return String(s.agent_id) === String(props.agentId)
      ? t('agentSettings.skillTagPrivate')
      : t('agentSettings.skillTagOwnedBy', {
        name: s.agent_name || t('agentSettings.skillTagOwnedByOther')
      })
  }
  return s.restricted ? t('agentSettings.skillTagWhitelist') : t('agentSettings.skillTagPublic')
}
const skillTagClass = (s) => ({
  warn: !s.agent_id && s.status !== 3 && s.restricted,
  muted: !!s.agent_id || s.status === 3
})

// 技能绑定：拉全部租户级技能 + 该 agent 已绑定的
const loadSkillBindings = async (id = props.agentId) => {
  if (!id) return
  try {
    const [sres, bres] = await Promise.all([
      fetch(adminApi.skills.base, {
        method: 'POST',
        headers: authHeaders(),
        body: JSON.stringify({ page: 1, limit: -1 })
      }),
      fetch(adminApi.agents.skillBindings(id), { headers: authHeaders() })
    ])
    const sbody = await sres.json().catch(() => ({}))
    const bbody = await bres.json().catch(() => ({}))
    // 全量展示：租户级 + 各 agent 私有 + 已废弃，可绑定性由 isBindableSkill 判定
    allSkills.value = sbody?.data?.items || []
    boundSkillIds.value = bbody?.data?.skill_ids || []
  } catch (e) {
    console.error('failed to load skill bindings:', e)
  }
}

// 技能绑定即时保存（与调用授权同款：改动即保存）。
// 技能发现开关已改为 agent 属性，不走这里（见 saveSkillDiscovery）。
const saveSkillBindings = async () => {
  if (!props.agentId) return // 未创建：技能绑定需要 agent id
  try {
    const res = await fetch(adminApi.agents.skillBindings(props.agentId), {
      method: 'PUT',
      headers: authHeaders(),
      body: JSON.stringify({ skill_ids: boundSkillIds.value })
    })
    const body = await res.json().catch(() => ({}))
    if (body.code !== 200) throw new Error(formatError(body) || t('agentSettings.skillBindSaveFailed'))
  } catch (e) {
    ElMessage.error(e.message || t('agentSettings.skillBindSaveFailed'))
    await loadSkillBindings() // 落库失败 → 回到服务端的真实状态，别让本地勾选与服务端漂移
  }
}

// ===== 技能选择侧栏（与工具授权同款交互） =====
const isSkillBound = (id) => boundSkillIds.value.some((x) => String(x) === String(id))
// tag 里显示技能名：技能被删后 id 还在库里时退回显示 id，至少不空白
const skillName = (id) => allSkills.value.find((s) => String(s.id) === String(id))?.name || id

const filteredSkills = computed(() => {
  const q = skillSearch.value.trim().toLowerCase()
  if (!q) return allSkills.value
  return allSkills.value.filter((s) =>
    String(s.name || '').toLowerCase().includes(q) ||
    String(s.description || '').toLowerCase().includes(q))
})

const openSkillPicker = async () => {
  if (!props.agentId) return
  await loadSkillBindings() // 每次打开都取最新的技能列表与绑定
  grantPickerVisible.value = false // 两个侧栏互斥，避免叠在一起
  skillSearch.value = ''
  skillPickerVisible.value = true
  await nextTick()
  updatePickerRect() // 按设置面板当前位置定位
}

// 点一项：已绑定就解绑，未绑定就绑定；立即保存（与调用授权同款）
const toggleSkillBinding = async (id) => {
  if (isSkillBound(id)) {
    boundSkillIds.value = boundSkillIds.value.filter((x) => String(x) !== String(id))
  } else {
    boundSkillIds.value = [...boundSkillIds.value, id]
  }
  await saveSkillBindings()
}

const unbindSkill = async (id) => {
  boundSkillIds.value = boundSkillIds.value.filter((x) => String(x) !== String(id))
  await saveSkillBindings()
}

// 「允许发现未绑定的技能」（agents.skill_discovery）：agent 属性，改动即保存（与改名/改描述同款）
const saveSkillDiscovery = async (val) => {
  if (!props.agentId) return // 未创建：随创建流程一起落库
  const next = !!val
  try {
    const res = await fetch(adminApi.agents.update + props.agentId, {
      method: 'PUT',
      headers: authHeaders(),
      body: JSON.stringify({ ...props.agent, skill_discovery: next })
    })
    const body = await res.json().catch(() => ({}))
    if (body.code !== 200) throw new Error(formatError(body) || t('agentSettings.saveFailed'))
    ElMessage.success(next ? t('agentSettings.discoveryOn') : t('agentSettings.discoveryOff'))
  } catch (e) {
    props.agent.skill_discovery = !next // 回滚开关
    ElMessage.error(e.message || t('agentSettings.saveFailed'))
  }
}

// Default reply language (agents.default_language): an agent attribute, saved on change like the two switches.
//
// It is a definition column of its own, not a key inside config, so the value is read straight off the agent object and
// written back with the same object - no config round-trip, and no way to wipe another key by saving it.
const LANGUAGE_LABEL_KEYS = {
  'zh-CN': 'langZhCN', en: 'langEn', ja: 'langJa', ko: 'langKo', fr: 'langFr',
  de: 'langDe', es: 'langEs', pt: 'langPt', ru: 'langRu', ar: 'langAr', it: 'langIt'
}

// The list is a convenience set, not a whitelist (the backend accepts any valid BCP-47 tag), so a stored tag outside it is
// appended as itself - otherwise the select would render blank and look like "not configured" while a value is stored.
const languageOptions = computed(() => {
  const options = [
    { value: '', label: t('agentSettings.langFollowCaller') },
    ...Object.keys(LANGUAGE_LABEL_KEYS).map(code => ({ value: code, label: t('agentSettings.' + LANGUAGE_LABEL_KEYS[code]) }))
  ]
  const current = defaultLanguage.value
  if (current && !LANGUAGE_LABEL_KEYS[current]) options.push({ value: current, label: current })
  return options
})

const defaultLanguage = ref(props.agent?.default_language || '')

// The parent replaces the agent object whenever it opens another agent or loads a version, so follow it instead of
// keeping the previous agent's value on screen.
watch(() => props.agent?.default_language, () => {
  defaultLanguage.value = props.agent?.default_language || ''
})

const saveDefaultLanguage = async (val) => {
  if (!props.agentId) return
  const tag = val || ''
  try {
    const res = await fetch(adminApi.agents.update + props.agentId, {
      method: 'PUT',
      headers: authHeaders(),
      body: JSON.stringify({ ...props.agent, default_language: tag })
    })
    const body = await res.json().catch(() => ({}))
    if (body.code !== 200) throw new Error(formatError(body) || t('agentSettings.saveFailed'))
    // Keep the local copy in sync: the next rename / description save echoes {...agent} and would otherwise undo this.
    props.agent.default_language = tag
    ElMessage.success(t('agentSettings.langSaved'))
  } catch (e) {
    defaultLanguage.value = props.agent?.default_language || '' // roll back to what is still stored
    ElMessage.error(e.message || t('agentSettings.saveFailed'))
  }
}

// 「超级权限」（agents.is_supervisor）：与内置超级智能体同级，改动即保存。
// 走 update 接口的单列分支（后端 SetSupervisor）—— UpdateAgent 是全字段覆盖，
// 但刻意不写这一列，所以这里提交整份表单也不会误清零别人。
const saveSupervisor = async (val) => {
  if (!props.agentId) return // 未创建：随创建流程一起落库
  const next = !!val
  try {
    const res = await fetch(adminApi.agents.update + props.agentId, {
      method: 'PUT',
      headers: authHeaders(),
      body: JSON.stringify({ ...props.agent, is_supervisor: next })
    })
    const body = await res.json().catch(() => ({}))
    if (body.code !== 200) throw new Error(formatError(body) || t('agentSettings.saveFailed'))
    emit('supervisor-change', next)
    if (next) {
      // 后端在 SetSupervisor 里已经把起步套件补齐（core 工具组 + 不限制工具 +
      // 技能发现 + 三份 authoring 技能）。授权草稿与可引用目录是按 agent 缓存的，
      // 这里必须先失效再重拉，否则「工具配置」页还停在授予前那一套（12 条单工具）。
      resetToolGrantCache()
      await Promise.all([loadSkillBindings(props.agentId), loadToolGrants()])
      ElMessage.success(t('agentSettings.supervisorGranted'))
    } else {
      // 撤销不回滚工具与技能：它们留在库里，但管理类工具在执行层已不可用
      ElMessage.success(t('agentSettings.supervisorRevoked'))
    }
    emit('refresh-list')
  } catch (e) {
    props.agent.is_supervisor = !next // 回滚开关
    ElMessage.error(e.message || t('agentSettings.saveFailed'))
  }
}

// 换 agent：授权草稿 / 可引用目录是按 agent 缓存的，进编辑器时必须先失效；
// 面板若还开着，关系与技能绑定也要按新 agent 重读
watch(() => props.agentId, async () => {
  resetToolGrantCache()
  boundSkillIds.value = []
  allSkills.value = []
  if (!props.visible || !props.agentId) return
  await Promise.all([loadRelations(), loadSkillBindings()])
})

// 父组件手动刷新：面板开着就把关系 / 授权 / 生效信息重读一遍
watch(() => props.refreshToken, async (token) => {
  if (!token || !props.visible || !props.agentId) return
  resetToolGrantCache()
  await Promise.all([loadRelations(), loadSkillBindings()])
  if (settingsTab.value === 'tools') {
    await loadToolGrants()
    await loadEffectiveInfo()
  }
})
</script>

<style scoped>
/* 语言下拉的弹层：设置面板本身是 body 级浮层（z-index 3200），而 Element Plus 的 popper 从 2000 起，
   会被面板压在下面 —— 现象就是「点下拉没反应 / 打开也没内容」。弹层 teleport 到 body，只能用全局选择器抬高层级。 */
:global(.agent-lang-select-popper) {
  z-index: 5000 !important;
}

/* ========== 智能体设置面板（对齐工作流试运行面板） ========== */
.settings-panel {
  /* teleport 到 body + fixed：脱离 .agents / .content-panel 的 overflow:hidden，
     不会再因为内容区变窄（会话窗口占宽）而被裁剪。
     位置/尺寸由内联样式按编辑区容器 rect 计算（updateSettingsRect）。 */
  position: fixed;
  z-index: 3200;
  background: #ffffff;
  border-radius: 6px 0 0 6px;
  box-shadow: -4px 0 20px rgba(0, 0, 0, 0.2);
  display: flex;
  flex-direction: column;
  overflow: hidden;
}

.settings-field {
  margin-bottom: 16px;
}

/* 绑定行：左侧按钮拉出选择侧栏，右侧平铺已绑定的 tag（技能 / 谁能调用它 / 它能调用谁 共用） */
.bind-row {
  display: flex;
  align-items: flex-start;
  gap: 8px;
}
.bind-tags {
  flex: 1;
  min-width: 0;
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
  padding-top: 2px;
}
.bind-tags-empty {
  font-size: 12px;
  color: #b1b3b8;
  line-height: 24px;
}

.settings-field .field-label {
  font-size: 13px;
  color: #606266;
  margin-bottom: 6px;
}

.settings-footer {
  display: flex;
  justify-content: flex-end;
  gap: 8px;
  padding: 12px 16px;
  border-top: 1px solid #f0f0f0;
  background: #fff;
}

.settings-tabs {
  display: flex;
  gap: 16px;
  flex: 1;
}

.settings-tab-btn {
  padding: 6px 4px;
  border: none;
  background: transparent;
  font-size: 14px;
  font-weight: 600;
  color: #666;
  cursor: pointer;
  position: relative;
}

.settings-tab-btn:hover {
  color: #1890ff;
}

.settings-tab-btn.active {
  color: #1890ff;
}

.settings-tab-btn.active::after {
  content: '';
  position: absolute;
  bottom: -1px;
  left: 0;
  right: 0;
  height: 2px;
  background: #1890ff;
}

.tools-actions {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-bottom: 10px;
}

.tools-count {
  margin-left: auto;
  font-size: 12px;
  color: #909399;
}

.tools-list {
  display: flex;
  flex-direction: column;
  gap: 8px;
}

.tool-item {
  display: flex;
  align-items: center;
  gap: 12px;
  padding: 8px 10px;
  border: 1px solid #f0f0f0;
  border-radius: 6px;
}

.tool-item-main {
  flex: 1;
  min-width: 0;
}

.tool-name {
  font-size: 13px;
  font-weight: 600;
  color: #333;
}

.tool-desc {
  font-size: 12px;
  color: #909399;
  margin-top: 2px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

/* ========== 工具授权（工具配置 tab / 选择弹窗 / 预览） ========== */
.ref-tag {
  display: inline-block;
  margin-left: 6px;
  padding: 0 5px;
  font-size: 11px;
  font-weight: 400;
  line-height: 16px;
  color: #909399;
  background: #f4f4f5;
  border-radius: 3px;
}
.ref-tag.warn {
  color: #e6a23c;
  background: #fdf6ec;
}
/* muted = 列出来但不可绑定（私有 / 已废弃）：压到最弱，不与可选项抢注意力 */
.ref-tag.muted {
  color: #b1b3b8;
  background: #f7f8fa;
}
/* core = 超级智能体的常驻组：删掉就少一整类能力，用主色提醒「这不是普通授权项」 */
.ref-tag.core {
  color: #409eff;
  background: #ecf5ff;
}
.tools-empty {
  display: flex;
  flex-direction: column;
  gap: 8px;
  align-items: flex-start;
}
.tools-save-bar {
  display: flex;
  align-items: center;
  gap: 10px;
  margin-top: 14px;
  padding-top: 12px;
  border-top: 1px solid #f0f0f0;
}
/* 分组块（工具配置三段 / 生效详情）：与上方文字、上一组都留出间距（第一组也有效） */
.grant-section {
  margin-top: 14px;
}
/* 组 / 工具行的图标（组深一点更醒目，工具淡一点） */
.grant-group-icon {
  flex-shrink: 0;
  color: #909399;
}
.grant-tool-icon {
  flex-shrink: 0;
  color: #c0c4cc;
}
.grant-section-title {
  display: flex;
  align-items: center;
  gap: 4px;
  margin-bottom: 6px;
  font-size: 12px;
  font-weight: 600;
  color: #606266;
}
/* 可点击折叠的组标题（生效详情） */
.grant-section-title.clickable {
  cursor: pointer;
  user-select: none;
}
.grant-section-title.clickable:hover {
  color: #409eff;
}
/* 生效详情视图的顶部（返回 + 标题 + 刷新） */
.view-back {
  display: flex;
  align-items: center;
  gap: 10px;
  margin-bottom: 12px;
}
.view-title {
  font-size: 13px;
  font-weight: 600;
  color: #333;
}
.view-refresh {
  margin-left: auto;
}
/* 配置视图底部的常驻生效摘要 */
.effective-bar {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-top: 10px;
  padding: 8px 10px;
  background: #fafafa;
  border-radius: 6px;
}
.effective-text {
  font-size: 12px;
  color: #606266;
}
.effective-detail {
  margin-left: auto;
}
.dirty-tip {
  font-size: 12px;
  color: #e6a23c;
}
/* 「添加工具 / 组」侧栏：与设置面板同款，贴在设置面板左侧。
   teleport 到 body + fixed 定位 + 高 z-index：不会被内容区 overflow 裁剪，也不会被左侧对话窗口遮挡；
   位置/尺寸由内联样式按设置面板的实际 rect 计算。 */
.picker-panel {
  position: fixed;
  z-index: 4000; /* body 级浮层：高于 Element Plus 动态分配的弹层（2000 起、随交互递增） */
  background: #ffffff;
  border-radius: 6px;
  box-shadow: -4px 0 20px rgba(0, 0, 0, 0.2);
  display: flex;
  flex-direction: column;
  overflow: hidden;
}
.picker-panel .panel-body {
  display: flex;
  flex-direction: column; /* 整体滚动（.panel-body 自带 overflow-y:auto） */
}
/* 三个区（tab）：必须导入 / 可使用 / 拒绝 */
.picker-tabs {
  display: flex;
  gap: 6px;
}
.picker-tab {
  flex: 1;
  padding: 6px 4px;
  font-size: 12px;
  color: #606266;
  background: #fff;
  border: 1px solid #dcdfe6;
  border-radius: 6px;
  cursor: pointer;
}
.picker-tab:hover {
  border-color: #c6e2ff;
  color: #409eff;
}
.picker-tab.active {
  border-color: #409eff;
  background: #ecf5ff;
  color: #409eff;
  font-weight: 600;
}
.picker-tab-count {
  margin-left: 4px;
  font-size: 11px;
  color: #909399;
}
.picker-tab.active .picker-tab-count {
  color: #409eff;
}
.picker-block-title {
  margin: 12px 0 6px;
  font-size: 12px;
  font-weight: 600;
  color: #606266;
}
.picker-block-hint {
  margin-left: 6px;
  font-weight: 400;
  color: #909399;
}
.picker-toolbar {
  display: flex;
  align-items: center;
  gap: 10px;
  margin-bottom: 10px;
  flex-wrap: wrap;
}
.picker-kinds {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
}
/* 组卡片：点头部整组加入，点组内行只加该工具 */
.picker-cards {
  display: flex;
  flex-direction: column;
  gap: 10px;
}
.picker-card {
  border: 1px solid #ebeef5;
  border-radius: 8px;
  overflow: hidden;
}
.picker-card.active {
  border-color: #409eff;
}
/* 不可选（私有 / 废弃技能）：整卡压暗、去掉 hover 与手型，但仍可读 */
.picker-card.disabled {
  opacity: 0.55;
}
.picker-card.disabled .picker-card-head {
  cursor: not-allowed;
  background: #fafafa;
}
.picker-card.disabled .picker-card-head:hover {
  background: #fafafa;
}
.picker-card-head {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 8px 10px;
  cursor: pointer;
  background: #fafafa;
}
.picker-card-head:hover {
  background: #f5faff;
}
/* 收起/展开箭头：点箭头只切展开，不影响整组选中 */
.picker-caret {
  color: #909399;
  cursor: pointer;
  transition: transform 0.2s;
}
.picker-caret.open {
  transform: rotate(90deg);
}
.picker-card.active .picker-card-head {
  background: #ecf5ff;
}
.picker-card-body {
  display: flex;
  flex-direction: column;
}
.picker-tool {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 5px 10px 5px 22px;
  font-size: 12px;
  color: #606266;
  cursor: pointer;
  border-top: 1px solid #f5f5f5;
}
.picker-tool:hover {
  background: #f5faff;
}
.picker-tool.on {
  color: #409eff;
}
.picker-tool.covered {
  color: #67c23a;
  cursor: default;
}
.picker-tool-name {
  flex: 1;
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
/* 工具简述：最多 20 个字，空间不够时自己收缩省略 */
.picker-tool-desc {
  flex: 0 1 auto;
  min-width: 0;
  max-width: 180px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  font-size: 11px;
  color: #a8abb2;
}
.picker-tool-mark {
  color: #c0c4cc;
}
.picker-tool.on .picker-tool-mark {
  color: #409eff;
}
.picker-tool.covered .picker-tool-mark {
  color: #67c23a;
}
.picker-check {
  color: #409eff;
}
.picker-footer {
  display: flex;
  align-items: center;
  gap: 10px;
  flex-wrap: wrap;
  padding: 10px 16px;
  border-top: 1px solid #f0f0f0;
}
.preview-warn {
  margin-top: 6px;
  padding: 6px 8px;
  font-size: 12px;
  color: #e6a23c;
  background: #fdf6ec;
  border-radius: 4px;
}

/* ===== 面板外壳：设置面板与「工具授权」侧栏共用同一套 ===== */
.panel-resizer {
  position: absolute;
  left: 0;
  top: 0;
  bottom: 0;
  width: 6px;
  cursor: col-resize;
  background: transparent;
  z-index: 10;
}

.panel-resizer:hover {
  background: #e0e0e0;
}

.panel-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
  padding: 6px 16px;
  color: black;
  gap: 12px;
}

.panel-title {
  font-weight: 600;
  font-size: 14px;
}

.close-icon {
  cursor: pointer;
  padding: 4px;
  border-radius: 4px;
  font-size: 18px;
}

.close-icon:hover {
  background: rgba(0, 0, 0, 0.1);
}

.panel-body {
  flex: 1;
  overflow-y: auto;
  padding: 16px;
  min-height: 0;
  /* 预留滚动条位置：内容高度变化（切区/搜索/收起展开）时不会因滚动条出现或消失而左右晃动 */
  scrollbar-gutter: stable;
  /* 滚动条平时隐形，鼠标进入面板才浮现（宽度与位置始终占着，不会让内容跳动） */
  scrollbar-width: thin;
  scrollbar-color: transparent transparent;
}
.panel-body:hover {
  scrollbar-color: #c0c4cc transparent;
}
.panel-body::-webkit-scrollbar {
  width: 8px;
  height: 8px;
}
.panel-body::-webkit-scrollbar-track {
  background: transparent;
}
.panel-body::-webkit-scrollbar-thumb {
  background: transparent;
  border-radius: 4px;
}
.panel-body:hover::-webkit-scrollbar-thumb {
  background: #c0c4cc;
}
.panel-body:hover::-webkit-scrollbar-thumb:hover {
  background: #a8abb2;
}

/* slot 内容里的灰字说明 / 工具条目的删除图标 */
.editor-tip {
  font-size: 12px;
  color: #909399;
  margin-top: 6px;
}

.node-del {
  margin-left: 4px;
  color: #c0c4cc;
  font-size: 12px;
}

.node-del:hover {
  color: #f56c6c;
}

</style>
