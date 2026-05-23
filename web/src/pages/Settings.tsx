import { useCallback, useEffect, useMemo, useState, type ChangeEvent, type CSSProperties } from 'react';
import { useNavigate } from 'react-router-dom';
import {
  cancelAgentSubscriptionFlow,
  continueAgentSubscriptionFlow,
  deleteAgentCustomProvider,
  deleteAgentProviderCredential,
  deleteApiKey,
  getAgentAuthProviders,
  getAgentCustomProviders,
  getAgentSubscriptionFlow,
  getApiKeyStatus,
  getServerConfig,
  logout,
  setAgentProviderApiKey,
  setApiKey,
  startAgentProviderSubscription,
  upsertAgentCustomProvider,
  type AgentAuthProvider,
  type AgentCustomProvider,
  type AgentCustomProviderApi,
  type AgentOAuthFlowState,
} from '../api/client';

type CustomForm = {
  provider: string;
  name: string;
  baseUrl: string;
  api: AgentCustomProviderApi;
  apiKey: string;
  modelId: string;
  modelName: string;
  reasoning: boolean;
  thinkingPreset: 'standard' | 'deepseek' | 'none';
  contextWindow: string;
  maxTokens: string;
};

const defaultCustomForm: CustomForm = {
  provider: 'litellm',
  name: 'LiteLLM',
  baseUrl: 'http://127.0.0.1:4000/v1',
  api: 'openai-responses',
  apiKey: '',
  modelId: 'openai/gpt-5.5',
  modelName: 'GPT 5.5 via LiteLLM',
  reasoning: true,
  thinkingPreset: 'standard',
  contextWindow: '128000',
  maxTokens: '16384',
};

const callbackSubscriptionProviders = new Set(['anthropic', 'openai-codex']);

function sourceLabel(source?: AgentAuthProvider['source']) {
  switch (source) {
    case 'stored':
      return 'Stored';
    case 'runtime':
      return 'Runtime';
    case 'environment':
      return 'Environment';
    case 'fallback':
      return 'Fallback';
    case 'models_json_key':
      return 'Models JSON';
    case 'models_json_command':
      return 'Command';
    default:
      return 'Not set';
  }
}

function apiLabel(api: AgentCustomProviderApi) {
  switch (api) {
    case 'openai-completions':
      return 'OpenAI Chat';
    case 'openai-responses':
      return 'OpenAI Responses';
    case 'anthropic-messages':
      return 'Anthropic Messages';
  }
}

function providerSortScore(provider: AgentAuthProvider) {
  if (provider.configured) return 0;
  if (provider.provider === 'anthropic') return 1;
  if (provider.provider === 'openai-codex') return 2;
  if (provider.provider === 'openai') return 3;
  if (provider.provider === 'google') return 4;
  return 5;
}

function providerTitle(provider: AgentAuthProvider) {
  if (!provider.name || provider.name === provider.provider) return provider.provider;
  return `${provider.name} (${provider.provider})`;
}

function isFlowTerminal(flow?: AgentOAuthFlowState | null) {
  return Boolean(flow && ['complete', 'error', 'cancelled'].includes(flow.status));
}

function flowStatusLabel(status: AgentOAuthFlowState['status']) {
  switch (status) {
    case 'starting':
      return 'Starting';
    case 'prompt':
      return 'Input needed';
    case 'auth':
    case 'waiting':
      return 'Waiting';
    case 'complete':
      return 'Connected';
    case 'error':
      return 'Error';
    case 'cancelled':
      return 'Cancelled';
  }
}

function flowStatusStyle(status: AgentOAuthFlowState['status']): CSSProperties {
  if (status === 'complete') return { color: 'var(--green)', borderColor: 'rgba(61, 220, 132, 0.35)' };
  if (status === 'error' || status === 'cancelled') return { color: 'var(--red)', borderColor: 'rgba(255, 107, 107, 0.35)' };
  if (status === 'prompt') return { color: 'var(--yellow)', borderColor: 'rgba(245, 197, 24, 0.35)' };
  return { color: 'var(--cyan)', borderColor: 'rgba(0, 229, 255, 0.35)' };
}

function thinkingMap(form: CustomForm) {
  if (!form.reasoning || form.thinkingPreset === 'none') return undefined;
  if (form.thinkingPreset === 'deepseek') {
    return {
      minimal: null,
      low: null,
      medium: null,
      high: 'high',
      xhigh: 'max',
    };
  }
  return {
    off: 'none',
    minimal: 'minimal',
    low: 'low',
    medium: 'medium',
    high: 'high',
    xhigh: 'xhigh',
  };
}

function compatFor(form: CustomForm): Record<string, unknown> | undefined {
  if (form.api === 'openai-responses') {
    return {
      thinkingFormat: 'openai',
      supportsReasoningEffort: form.reasoning,
      maxTokensField: 'max_output_tokens',
      supportsPromptCacheKey: form.modelId === 'openai/gpt-5.5',
      promptCacheRetention: form.modelId === 'openai/gpt-5.5' ? '24h' : undefined,
    };
  }
  if (form.thinkingPreset === 'deepseek') {
    return { thinkingFormat: 'deepseek', maxTokensField: 'max_tokens' };
  }
  if (form.api === 'openai-completions') {
    return {
      supportsDeveloperRole: false,
      supportsReasoningEffort: form.reasoning,
      supportsUsageInStreaming: false,
      maxTokensField: 'max_tokens',
    };
  }
  return undefined;
}

function modelFromCustom(provider: AgentCustomProvider): CustomForm {
  const model = provider.models[0];
  const api = model?.api || provider.api || 'openai-responses';
  const hasDeepSeekMap = model?.thinkingLevelMap?.xhigh === 'max';
  return {
    provider: provider.provider,
    name: provider.name || provider.provider,
    baseUrl: provider.baseUrl || '',
    api,
    apiKey: '',
    modelId: model?.id || '',
    modelName: model?.name || model?.id || '',
    reasoning: Boolean(model?.reasoning),
    thinkingPreset: model?.reasoning ? (hasDeepSeekMap ? 'deepseek' : 'standard') : 'none',
    contextWindow: String(model?.contextWindow || 128000),
    maxTokens: String(model?.maxTokens || 16384),
  };
}

/** Settings page for runtime credentials, egress, and account actions. */
export default function Settings() {
  const navigate = useNavigate();
  const [agentBackend, setAgentBackend] = useState<'opencode' | 'pi' | null>(null);
  const [keySet, setKeySet] = useState<boolean | null>(null);
  const [providers, setProviders] = useState<AgentAuthProvider[]>([]);
  const [customProviders, setCustomProviders] = useState<AgentCustomProvider[]>([]);
  const [selectedProvider, setSelectedProvider] = useState('');
  const [newKey, setNewKey] = useState('');
  const [subscriptionFlow, setSubscriptionFlow] = useState<AgentOAuthFlowState | null>(null);
  const [subscriptionInput, setSubscriptionInput] = useState('');
  const [subscriptionFallbackOpen, setSubscriptionFallbackOpen] = useState(false);
  const [customEditorOpen, setCustomEditorOpen] = useState(false);
  const [customForm, setCustomForm] = useState<CustomForm>(defaultCustomForm);
  const [saving, setSaving] = useState(false);
  const [customSaving, setCustomSaving] = useState(false);
  const [subscriptionBusy, setSubscriptionBusy] = useState(false);
  const [loadingProviders, setLoadingProviders] = useState(false);
  const [error, setError] = useState('');
  const [success, setSuccess] = useState('');

  const sortedProviders = useMemo(
    () =>
      [...providers].sort(
        (a, b) =>
          providerSortScore(a) - providerSortScore(b) ||
          b.availableModelCount - a.availableModelCount ||
          a.provider.localeCompare(b.provider),
      ),
    [providers],
  );

  const selected = sortedProviders.find((provider) => provider.provider === selectedProvider);
  const configuredCount = sortedProviders.filter((provider) => provider.configured).length;
  const selectedSupportsKey = selected?.supportsApiKey ?? false;
  const selectedSupportsSubscription = selected?.supportsSubscription ?? false;
  const canRemoveSelected = selected?.source === 'stored' || selected?.credentialType === 'oauth';
  const subscriptionActive = Boolean(subscriptionFlow && !isFlowTerminal(subscriptionFlow));

  const clearMessages = () => {
    setError('');
    setSuccess('');
  };

  const chooseProvider = useCallback((provider: string) => {
    setSelectedProvider(provider);
    setNewKey('');
    setSubscriptionInput('');
    setSubscriptionFallbackOpen(false);
    setSubscriptionFlow(null);
  }, []);

  const loadPiAuth = useCallback(async () => {
    setLoadingProviders(true);
    try {
      const [authRes, customRes] = await Promise.all([
        getAgentAuthProviders(),
        getAgentCustomProviders(),
      ]);
      setProviders(authRes.providers);
      setCustomProviders(customRes.providers);
      setSelectedProvider((current) => {
        if (current && authRes.providers.some((provider) => provider.provider === current)) {
          return current;
        }
        const preferred =
          authRes.providers.find((provider) => provider.configured) ||
          authRes.providers.find((provider) => provider.provider === 'anthropic') ||
          authRes.providers.find((provider) => provider.provider === 'openai-codex') ||
          authRes.providers.find((provider) => provider.provider === 'openai') ||
          authRes.providers[0];
        return preferred?.provider ?? '';
      });
    } finally {
      setLoadingProviders(false);
    }
  }, []);

  useEffect(() => {
    getServerConfig()
      .then(async (cfg) => {
        setAgentBackend(cfg.agentBackend || 'opencode');
        if (cfg.agentBackend === 'pi') {
          await loadPiAuth();
          return;
        }
        const res = await getApiKeyStatus();
        setKeySet(res.set);
      })
      .catch(() => {
        window.location.href = '/login';
      });
  }, [loadPiAuth]);

  useEffect(() => {
    if (!subscriptionFlow || isFlowTerminal(subscriptionFlow)) return;
    const timer = window.setInterval(() => {
      getAgentSubscriptionFlow(subscriptionFlow.id)
        .then(async (state) => {
          setSubscriptionFlow(state);
          if (state.status === 'complete') {
            setSubscriptionInput('');
            setSubscriptionFallbackOpen(false);
            setSuccess(`${state.providerName} subscription saved.`);
            await loadPiAuth();
          }
        })
        .catch((err: unknown) => {
          setError(err instanceof Error ? err.message : String(err));
        });
    }, 2500);
    return () => window.clearInterval(timer);
  }, [loadPiAuth, subscriptionFlow]);

  const handleProviderSelect = (event: ChangeEvent<HTMLSelectElement>) => {
    chooseProvider(event.target.value);
    event.currentTarget.blur();
  };

  const handleOpenCodeSave = async () => {
    if (!newKey.trim()) return;
    setSaving(true);
    clearMessages();
    try {
      await setApiKey(newKey.trim());
      setKeySet(true);
      setNewKey('');
      setSuccess('API key saved.');
    } catch (e: unknown) {
      setError(e instanceof Error ? e.message : 'Failed to save key');
    } finally {
      setSaving(false);
    }
  };

  const handleOpenCodeDelete = async () => {
    setSaving(true);
    clearMessages();
    try {
      await deleteApiKey();
      setKeySet(false);
      setSuccess('API key removed.');
    } catch (e: unknown) {
      setError(e instanceof Error ? e.message : 'Failed to remove key');
    } finally {
      setSaving(false);
    }
  };

  const handlePiSave = async () => {
    if (!selectedProvider || !newKey.trim() || !selectedSupportsKey) return;
    setSaving(true);
    clearMessages();
    try {
      await setAgentProviderApiKey(selectedProvider, newKey.trim());
      await loadPiAuth();
      setNewKey('');
      setSuccess(`${selected?.name || selectedProvider} API key saved.`);
    } catch (e: unknown) {
      setError(e instanceof Error ? e.message : 'Failed to save credential');
    } finally {
      setSaving(false);
    }
  };

  const handlePiDelete = async () => {
    if (!selectedProvider) return;
    setSaving(true);
    clearMessages();
    try {
      await deleteAgentProviderCredential(selectedProvider);
      await loadPiAuth();
      setSubscriptionFlow(null);
      setSuccess(`${selected?.name || selectedProvider} credential removed.`);
    } catch (e: unknown) {
      setError(e instanceof Error ? e.message : 'Failed to remove credential');
    } finally {
      setSaving(false);
    }
  };

  const handleStartSubscription = async () => {
    if (!selectedProvider || !selectedSupportsSubscription) return;
    setSubscriptionBusy(true);
    clearMessages();
    setSubscriptionInput('');
    setSubscriptionFallbackOpen(false);
    try {
      const state = await startAgentProviderSubscription(selectedProvider);
      setSubscriptionFlow(state);
      if (state.status === 'complete') {
        setSubscriptionFallbackOpen(false);
        setSuccess(`${state.providerName} subscription saved.`);
        await loadPiAuth();
      }
    } catch (e: unknown) {
      setError(e instanceof Error ? e.message : 'Failed to start subscription login');
    } finally {
      setSubscriptionBusy(false);
    }
  };

  const handleContinueSubscription = async () => {
    if (!subscriptionFlow) return;
    setSubscriptionBusy(true);
    clearMessages();
    try {
      const state = await continueAgentSubscriptionFlow(subscriptionFlow.id, subscriptionInput);
      setSubscriptionFlow(state);
      if (state.status === 'complete') {
        setSubscriptionInput('');
        setSubscriptionFallbackOpen(false);
        setSuccess(`${state.providerName} subscription saved.`);
        await loadPiAuth();
      }
    } catch (e: unknown) {
      setError(e instanceof Error ? e.message : 'Failed to continue subscription login');
    } finally {
      setSubscriptionBusy(false);
    }
  };

  const handleCancelSubscription = async () => {
    if (!subscriptionFlow) return;
    setSubscriptionBusy(true);
    clearMessages();
    try {
      await cancelAgentSubscriptionFlow(subscriptionFlow.id);
      setSubscriptionFlow(null);
      setSubscriptionInput('');
      setSubscriptionFallbackOpen(false);
    } catch (e: unknown) {
      setError(e instanceof Error ? e.message : 'Failed to cancel subscription login');
    } finally {
      setSubscriptionBusy(false);
    }
  };

  const updateCustomForm = <K extends keyof CustomForm>(key: K, value: CustomForm[K]) => {
    setCustomForm((current) => ({ ...current, [key]: value }));
  };

  const openCustomProviderEditor = (provider?: AgentCustomProvider) => {
    setCustomForm(provider ? modelFromCustom(provider) : defaultCustomForm);
    setCustomEditorOpen(true);
  };

  const closeCustomProviderEditor = () => {
    setCustomEditorOpen(false);
    setCustomForm(defaultCustomForm);
  };

  const handleSaveCustomProvider = async () => {
    const existing = customProviders.find((provider) => provider.provider === customForm.provider.trim());
    const contextWindow = Number(customForm.contextWindow);
    const maxTokens = Number(customForm.maxTokens);
    if (!customForm.provider.trim() || !customForm.baseUrl.trim() || !customForm.modelId.trim()) return;
    if (!customForm.apiKey.trim() && !existing?.apiKeyConfigured) return;
    if (!Number.isInteger(contextWindow) || contextWindow <= 0) {
      setError('Context window must be a positive integer.');
      return;
    }
    if (!Number.isInteger(maxTokens) || maxTokens <= 0) {
      setError('Max tokens must be a positive integer.');
      return;
    }

    setCustomSaving(true);
    clearMessages();
    try {
      const saved = await upsertAgentCustomProvider({
        provider: customForm.provider.trim(),
        name: customForm.name.trim() || customForm.provider.trim(),
        baseUrl: customForm.baseUrl.trim(),
        api: customForm.api,
        apiKey: customForm.apiKey.trim() || undefined,
        models: [
          {
            id: customForm.modelId.trim(),
            name: customForm.modelName.trim() || customForm.modelId.trim(),
            api: customForm.api,
            reasoning: customForm.reasoning,
            thinkingLevelMap: thinkingMap(customForm),
            input: ['text'],
            contextWindow,
            maxTokens,
            compat: compatFor(customForm),
          },
        ],
      });
      await loadPiAuth();
      setCustomForm((current) => ({ ...current, apiKey: '' }));
      setCustomEditorOpen(false);
      setSuccess(`${saved.name || saved.provider} provider saved.`);
    } catch (e: unknown) {
      setError(e instanceof Error ? e.message : 'Failed to save custom provider');
    } finally {
      setCustomSaving(false);
    }
  };

  const handleDeleteCustomProvider = async (provider: string) => {
    const deletingOpenProvider = customForm.provider === provider;
    setCustomSaving(true);
    clearMessages();
    try {
      await deleteAgentCustomProvider(provider);
      await loadPiAuth();
      if (deletingOpenProvider) closeCustomProviderEditor();
      setSuccess(`${provider} custom provider removed.`);
    } catch (e: unknown) {
      setError(e instanceof Error ? e.message : 'Failed to remove custom provider');
    } finally {
      setCustomSaving(false);
    }
  };

  const isPi = agentBackend === 'pi';
  const subscriptionInputPlaceholder =
    subscriptionFlow?.status === 'prompt'
      ? subscriptionFlow.prompt?.placeholder || 'Value'
      : 'Paste redirect URL or authorization code';
  const subscriptionNeedsInput =
    subscriptionFlow?.status === 'prompt' ||
    (subscriptionFlow?.status === 'auth' && subscriptionFallbackOpen);
  const subscriptionCanUseFallback =
    subscriptionFlow?.status === 'auth' &&
    callbackSubscriptionProviders.has(subscriptionFlow.provider) &&
    !isFlowTerminal(subscriptionFlow);
  const showSubscriptionInstructions =
    Boolean(subscriptionFlow?.instructions) &&
    (subscriptionFlow?.status !== 'auth' ||
      subscriptionFallbackOpen ||
      !callbackSubscriptionProviders.has(subscriptionFlow.provider));
  const subscriptionContinueDisabled =
    subscriptionBusy ||
    !subscriptionFlow ||
    !subscriptionNeedsInput ||
    (!subscriptionInput.trim() && subscriptionFlow.status !== 'prompt') ||
    (!subscriptionInput.trim() && subscriptionFlow.status === 'prompt' && !subscriptionFlow.prompt?.allowEmpty);

  return (
    <div style={styles.container}>
      <header style={styles.header}>
        <span style={styles.wordmark}>APPX</span>
        <div style={styles.headerActions}>
          <button
            data-btn="text-nav"
            style={{ ...styles.navBtn, color: 'var(--muted)' }}
            onClick={() => logout().then(() => { window.location.href = '/login'; })}
          >
            Logout
          </button>
        </div>
      </header>

      <main style={styles.main}>
        <div style={styles.pageHeader}>
          <button style={styles.backBtn} onClick={() => navigate('/')} aria-label="Back to dashboard">&#8592;</button>
          <span style={styles.pageTitle}>SETTINGS</span>
        </div>

        <div style={styles.card}>
          {isPi ? (
            <>
              <h3 style={styles.cardTitle}>Agent Credentials</h3>
              <p style={styles.description}>Pi auth for the agent service user.</p>

              <div style={styles.statusRow}>
                <span style={styles.statusLabel}>Status</span>
                {loadingProviders ? (
                  <span style={styles.statusMuted}>Loading...</span>
                ) : configuredCount > 0 ? (
                  <span style={{ ...styles.status, color: 'var(--green)' }}>
                    <span style={{ ...styles.dot, background: 'var(--green)' }} />
                    {configuredCount} configured
                  </span>
                ) : (
                  <span style={{ ...styles.status, color: 'var(--muted)' }}>
                    <span style={{ ...styles.dot, background: 'var(--muted)' }} />
                    None stored
                  </span>
                )}
              </div>

              {error && <div style={styles.error}>{error}</div>}
              {success && <div style={styles.successMsg}>{success}</div>}

              <div style={styles.inputRow}>
                <select
                  style={styles.select}
                  value={selectedProvider}
                  onChange={handleProviderSelect}
                  disabled={loadingProviders || sortedProviders.length === 0}
                >
                  {sortedProviders.map((provider) => (
                    <option key={provider.provider} value={provider.provider}>
                      {providerTitle(provider)}
                    </option>
                  ))}
                </select>
                <input
                  style={styles.input}
                  type="password"
                  placeholder={selected ? `${selected.name || selected.provider} API key` : 'Provider API key'}
                  value={newKey}
                  onChange={(event) => setNewKey(event.target.value)}
                  onKeyDown={(event) => event.key === 'Enter' && handlePiSave()}
                  disabled={!selectedSupportsKey}
                />
                <button
                  data-btn="primary"
                  style={styles.saveBtn}
                  onClick={handlePiSave}
                  disabled={saving || !selectedProvider || !newKey.trim() || !selectedSupportsKey}
                >
                  {saving ? 'Saving...' : 'Save'}
                </button>
              </div>

              {selected && (
                <div style={styles.providerMeta}>
                  <span style={styles.statusLabel}>Selected</span>
                  <span style={selected.configured ? styles.providerConfigured : styles.providerUnset}>
                    {sourceLabel(selected.source)}
                  </span>
                  <span style={styles.statusMuted}>
                    {selected.availableModelCount}/{selected.modelCount} models
                  </span>
                </div>
              )}

              <div style={styles.actionRow}>
                <button
                  data-btn="outline-green"
                  style={styles.outlineBtn}
                  onClick={handleStartSubscription}
                  disabled={!selectedSupportsSubscription || subscriptionBusy || subscriptionActive || !selectedProvider}
                >
                  {subscriptionBusy ? 'Working...' : subscriptionActive ? 'Login in progress' : 'Subscription Login'}
                </button>
                {canRemoveSelected && (
                  <button
                    data-btn="text-red"
                    style={styles.removeBtn}
                    onClick={handlePiDelete}
                    disabled={saving}
                  >
                    Remove credential
                  </button>
                )}
              </div>

              {subscriptionFlow && (
                <div style={styles.flowPanel}>
                  <div style={styles.flowHeader}>
                    <div style={styles.flowTitleGroup}>
                      <span style={styles.statusLabel}>Browser login</span>
                      <span style={styles.flowProviderName}>{subscriptionFlow.providerName}</span>
                    </div>
                    <span style={{ ...styles.flowStatusPill, ...flowStatusStyle(subscriptionFlow.status) }}>
                      {flowStatusLabel(subscriptionFlow.status)}
                    </span>
                  </div>
                  {subscriptionFlow.authUrl && (
                    <div style={styles.loginRow}>
                      <a style={styles.loginLink} href={subscriptionFlow.authUrl} target="_blank" rel="noreferrer">
                        Open login
                      </a>
                      {!subscriptionFallbackOpen && !isFlowTerminal(subscriptionFlow) && (
                        <span style={styles.waitingText}>Waiting for login to finish</span>
                      )}
                    </div>
                  )}
                  {subscriptionFlow.prompt && (
                    <p style={styles.flowText}>{subscriptionFlow.prompt.message}</p>
                  )}
                  {showSubscriptionInstructions && (
                    <p style={styles.flowText}>{subscriptionFlow.instructions}</p>
                  )}
                  {subscriptionFlow.error && <div style={styles.error}>{subscriptionFlow.error}</div>}
                  {subscriptionFlow.progress.length > 0 && (
                    <div style={styles.progressText}>{subscriptionFlow.progress.at(-1)}</div>
                  )}
                  {subscriptionNeedsInput && (
                    <div style={styles.flowInputRow}>
                      <input
                        style={styles.input}
                        value={subscriptionInput}
                        placeholder={subscriptionInputPlaceholder}
                        onChange={(event) => setSubscriptionInput(event.target.value)}
                        onKeyDown={(event) => event.key === 'Enter' && !subscriptionContinueDisabled && handleContinueSubscription()}
                      />
                      <button
                        data-btn="primary"
                        style={styles.saveBtn}
                        onClick={handleContinueSubscription}
                        disabled={subscriptionContinueDisabled}
                      >
                        {subscriptionFlow.status === 'prompt' ? 'Continue' : 'Submit'}
                      </button>
                    </div>
                  )}
                  {!isFlowTerminal(subscriptionFlow) && (
                    <div style={styles.flowActions}>
                      {subscriptionCanUseFallback && !subscriptionFallbackOpen && (
                        <button
                          data-btn="text"
                          type="button"
                          style={styles.fallbackBtn}
                          onClick={() => setSubscriptionFallbackOpen(true)}
                        >
                          Use manual fallback
                        </button>
                      )}
                      <button
                        data-btn="text-red"
                        type="button"
                        style={styles.flowCancelBtn}
                        onClick={handleCancelSubscription}
                        disabled={subscriptionBusy}
                      >
                        Cancel login
                      </button>
                    </div>
                  )}
                </div>
              )}

              <div style={styles.providerList}>
                {sortedProviders.length === 0 ? (
                  <span style={styles.emptyText}>No providers reported by agent-server</span>
                ) : (
                  sortedProviders.slice(0, 14).map((provider) => (
                    <button
                      key={provider.provider}
                      type="button"
                      style={
                        provider.provider === selectedProvider
                          ? styles.providerRowActive
                          : styles.providerRow
                      }
                      onMouseDown={(event) => event.preventDefault()}
                      onClick={() => chooseProvider(provider.provider)}
                    >
                      <span style={styles.providerName}>{provider.name || provider.provider}</span>
                      <span style={provider.configured ? styles.providerConfigured : styles.providerUnset}>
                        {sourceLabel(provider.source)}
                      </span>
                      <span style={styles.providerCount}>
                        {provider.availableModelCount}/{provider.modelCount}
                      </span>
                    </button>
                  ))
                )}
              </div>

              <div style={styles.section}>
                <div style={styles.sectionHeader}>
                  <div style={styles.sectionTitleGroup}>
                    <h3 style={{ ...styles.cardTitle, margin: 0 }}>Custom Provider</h3>
                    <span style={styles.statusMuted}>
                      {customProviders.length === 1 ? '1 configured' : `${customProviders.length} configured`}
                    </span>
                  </div>
                  <button
                    type="button"
                    style={styles.iconBtn}
                    aria-label={customEditorOpen ? 'Close custom provider editor' : 'Add custom provider'}
                    title={customEditorOpen ? 'Close' : 'Add custom provider'}
                    onClick={customEditorOpen ? closeCustomProviderEditor : () => openCustomProviderEditor()}
                  >
                    {customEditorOpen ? '\u00d7' : '+'}
                  </button>
                </div>

                {customProviders.length > 0 ? (
                  <div style={styles.customProviderList}>
                    {customProviders.map((provider) => (
                      <div key={provider.provider} style={styles.customProviderRow}>
                        <button
                          type="button"
                          style={styles.customProviderPick}
                          onMouseDown={(event) => event.preventDefault()}
                          onClick={() => openCustomProviderEditor(provider)}
                        >
                          <span style={styles.providerName}>{provider.name || provider.provider}</span>
                          <span style={styles.statusMuted}>{provider.modelCount} models</span>
                          <span style={provider.apiKeyConfigured ? styles.providerConfigured : styles.providerUnset}>
                            {provider.apiKeyConfigured ? 'Key stored' : 'No key'}
                          </span>
                        </button>
                        <button
                          data-btn="text-red"
                          style={styles.removeBtn}
                          onClick={() => handleDeleteCustomProvider(provider.provider)}
                          disabled={customSaving}
                        >
                          Remove
                        </button>
                      </div>
                    ))}
                  </div>
                ) : (
                  !customEditorOpen && <span style={styles.emptyText}>No custom providers</span>
                )}

                {customEditorOpen && (
                  <div style={styles.customEditor}>
                    <div style={styles.customGrid}>
                      <label style={styles.fieldLabel}>
                        Provider
                        <input
                          style={styles.input}
                          value={customForm.provider}
                          onChange={(event) => updateCustomForm('provider', event.target.value)}
                        />
                      </label>
                      <label style={styles.fieldLabel}>
                        Name
                        <input
                          style={styles.input}
                          value={customForm.name}
                          onChange={(event) => updateCustomForm('name', event.target.value)}
                        />
                      </label>
                      <label style={styles.fieldLabelLarge}>
                        Base URL
                        <input
                          style={styles.input}
                          value={customForm.baseUrl}
                          onChange={(event) => updateCustomForm('baseUrl', event.target.value)}
                        />
                      </label>
                      <label style={styles.fieldLabel}>
                        API
                        <select
                          style={styles.select}
                          value={customForm.api}
                          onChange={(event) => updateCustomForm('api', event.target.value as AgentCustomProviderApi)}
                        >
                          {(['openai-responses', 'openai-completions', 'anthropic-messages'] as AgentCustomProviderApi[]).map((api) => (
                            <option key={api} value={api}>{apiLabel(api)}</option>
                          ))}
                        </select>
                      </label>
                      <label style={styles.fieldLabel}>
                        API key
                        <input
                          style={styles.input}
                          type="password"
                          value={customForm.apiKey}
                          placeholder={
                            customProviders.find((provider) => provider.provider === customForm.provider)?.apiKeyConfigured
                              ? 'Stored'
                              : 'Required'
                          }
                          onChange={(event) => updateCustomForm('apiKey', event.target.value)}
                        />
                      </label>
                      <label style={styles.fieldLabelLarge}>
                        Model
                        <input
                          style={styles.input}
                          value={customForm.modelId}
                          onChange={(event) => updateCustomForm('modelId', event.target.value)}
                        />
                      </label>
                      <label style={styles.fieldLabelLarge}>
                        Model name
                        <input
                          style={styles.input}
                          value={customForm.modelName}
                          onChange={(event) => updateCustomForm('modelName', event.target.value)}
                        />
                      </label>
                      <label style={styles.fieldLabel}>
                        Context
                        <input
                          style={styles.input}
                          inputMode="numeric"
                          value={customForm.contextWindow}
                          onChange={(event) => updateCustomForm('contextWindow', event.target.value)}
                        />
                      </label>
                      <label style={styles.fieldLabel}>
                        Max tokens
                        <input
                          style={styles.input}
                          inputMode="numeric"
                          value={customForm.maxTokens}
                          onChange={(event) => updateCustomForm('maxTokens', event.target.value)}
                        />
                      </label>
                    </div>

                    <div style={styles.toggleRow}>
                      <label style={styles.checkboxLabel}>
                        <input
                          type="checkbox"
                          checked={customForm.reasoning}
                          onChange={(event) => updateCustomForm('reasoning', event.target.checked)}
                        />
                        Reasoning
                      </label>
                      <select
                        style={styles.compactSelect}
                        value={customForm.thinkingPreset}
                        onChange={(event) => updateCustomForm('thinkingPreset', event.target.value as CustomForm['thinkingPreset'])}
                        disabled={!customForm.reasoning}
                      >
                        <option value="standard">Standard thinking</option>
                        <option value="deepseek">DeepSeek thinking</option>
                        <option value="none">No thinking map</option>
                      </select>
                      <button
                        data-btn="primary"
                        style={styles.saveBtn}
                        onClick={handleSaveCustomProvider}
                        disabled={
                          customSaving ||
                          !customForm.provider.trim() ||
                          !customForm.baseUrl.trim() ||
                          !customForm.modelId.trim() ||
                          (!customForm.apiKey.trim() && !customProviders.find((provider) => provider.provider === customForm.provider.trim())?.apiKeyConfigured)
                        }
                      >
                        {customSaving ? 'Saving...' : 'Save provider'}
                      </button>
                    </div>
                  </div>
                )}
              </div>
            </>
          ) : (
            <>
              <h3 style={styles.cardTitle}>Legacy OpenCode Anthropic Key</h3>
              <p style={styles.description}>
                Used only when <code style={styles.code}>APPX_AGENT_BACKEND=opencode</code>.
              </p>

              <div style={styles.statusRow}>
                <span style={styles.statusLabel}>Status</span>
                {keySet === null ? (
                  <span style={styles.statusMuted}>Loading...</span>
                ) : keySet ? (
                  <span style={{ ...styles.status, color: 'var(--green)' }}>
                    <span style={{ ...styles.dot, background: 'var(--green)' }} />
                    Configured
                  </span>
                ) : (
                  <span style={{ ...styles.status, color: 'var(--red)' }}>
                    <span style={{ ...styles.dot, background: 'var(--red)' }} />
                    Not set
                  </span>
                )}
              </div>

              {error && <div style={styles.error}>{error}</div>}
              {success && <div style={styles.successMsg}>{success}</div>}

              <div style={styles.inputRow}>
                <input
                  style={styles.input}
                  type="password"
                  placeholder="sk-ant-..."
                  value={newKey}
                  onChange={(event) => setNewKey(event.target.value)}
                  onKeyDown={(event) => event.key === 'Enter' && handleOpenCodeSave()}
                />
                <button
                  data-btn="primary"
                  style={styles.saveBtn}
                  onClick={handleOpenCodeSave}
                  disabled={saving || !newKey.trim()}
                >
                  {saving ? 'Saving...' : 'Save'}
                </button>
              </div>

              {keySet && (
                <button
                  data-btn="text-red"
                  style={styles.removeBtn}
                  onClick={handleOpenCodeDelete}
                  disabled={saving}
                >
                  Remove key
                </button>
              )}
            </>
          )}
        </div>

        <div style={{ ...styles.card, marginTop: 16, cursor: 'pointer' }} onClick={() => navigate('/egress')}>
          <h3 style={styles.cardTitle}>Egress Control</h3>
          <p style={{ ...styles.description, margin: 0 }}>
            View outbound connection log and manage the allowlist for agent network access.
          </p>
        </div>
      </main>
    </div>
  );
}

const styles: Record<string, CSSProperties> = {
  container: {
    minHeight: '100vh',
  },
  header: {
    borderBottom: '1px solid var(--border)',
    padding: '14px 24px',
    display: 'flex',
    alignItems: 'center',
    justifyContent: 'space-between',
  },
  wordmark: {
    fontFamily: "'DM Sans', sans-serif",
    fontSize: 14,
    fontWeight: 500,
    letterSpacing: '0.35em',
    color: 'var(--text)',
  },
  headerActions: {
    display: 'flex',
    alignItems: 'center',
    gap: 4,
  },
  navBtn: {
    background: 'transparent',
    border: 'none',
    color: 'var(--muted)',
    padding: '5px 10px',
    fontSize: 13,
    cursor: 'pointer',
  },
  main: {
    padding: '28px 24px',
    maxWidth: 840,
    margin: '0 auto',
  },
  pageHeader: {
    display: 'flex',
    alignItems: 'center',
    gap: 10,
    marginBottom: 20,
  },
  backBtn: {
    background: 'transparent',
    border: 'none',
    color: 'var(--muted)',
    fontSize: 20,
    cursor: 'pointer',
    padding: '0 4px',
    lineHeight: 1,
  },
  pageTitle: {
    fontFamily: "'JetBrains Mono', monospace",
    fontSize: 11,
    letterSpacing: '0.12em',
    color: 'var(--muted)',
  },
  card: {
    background: 'var(--surface)',
    border: '1px solid var(--border)',
    borderRadius: 4,
    padding: '20px 22px',
  },
  cardTitle: {
    margin: '0 0 8px',
    fontSize: 14,
    fontWeight: 500,
    color: 'var(--text)',
  },
  description: {
    color: 'var(--muted)',
    fontSize: 13,
    lineHeight: 1.6,
    margin: '0 0 18px',
  },
  code: {
    fontFamily: "'JetBrains Mono', monospace",
    fontSize: 11,
    background: 'var(--bg)',
    padding: '1px 5px',
    borderRadius: 3,
    color: 'var(--text)',
  },
  statusRow: {
    display: 'flex',
    alignItems: 'center',
    gap: 10,
    marginBottom: 18,
    fontSize: 13,
  },
  statusLabel: {
    fontFamily: "'JetBrains Mono', monospace",
    fontSize: 10,
    letterSpacing: '0.1em',
    color: 'var(--muted)',
    textTransform: 'uppercase',
    whiteSpace: 'nowrap',
  },
  dot: {
    display: 'inline-block',
    width: 7,
    height: 7,
    borderRadius: '50%',
    marginRight: 6,
    flexShrink: 0,
  },
  status: {
    fontSize: 13,
    fontWeight: 500,
    display: 'flex',
    alignItems: 'center',
  },
  statusMuted: {
    color: 'var(--muted)',
    fontSize: 13,
    minWidth: 0,
  },
  inputRow: {
    display: 'grid',
    gridTemplateColumns: 'minmax(180px, 240px) minmax(0, 1fr) auto',
    gap: 8,
    marginBottom: 12,
  },
  select: {
    minWidth: 0,
    width: '100%',
    background: 'var(--bg)',
    border: '1px solid var(--border)',
    borderRadius: 4,
    padding: '8px 10px',
    color: 'var(--text)',
    fontSize: 13,
    outline: 'none',
  },
  compactSelect: {
    minWidth: 180,
    background: 'var(--bg)',
    border: '1px solid var(--border)',
    borderRadius: 4,
    padding: '8px 10px',
    color: 'var(--text)',
    fontSize: 13,
    outline: 'none',
  },
  input: {
    minWidth: 0,
    width: '100%',
    background: 'var(--bg)',
    border: '1px solid var(--border)',
    borderRadius: 4,
    padding: '8px 12px',
    color: 'var(--text)',
    fontSize: 13,
    outline: 'none',
  },
  saveBtn: {
    background: 'var(--blue)',
    border: 'none',
    color: '#fff',
    borderRadius: 4,
    padding: '8px 18px',
    fontSize: 13,
    fontWeight: 500,
    cursor: 'pointer',
    whiteSpace: 'nowrap',
  },
  outlineBtn: {
    background: 'transparent',
    border: '1px solid var(--green)',
    color: 'var(--green)',
    borderRadius: 4,
    padding: '8px 12px',
    fontSize: 13,
    fontWeight: 500,
    cursor: 'pointer',
    whiteSpace: 'nowrap',
  },
  actionRow: {
    display: 'flex',
    alignItems: 'center',
    gap: 12,
    flexWrap: 'wrap',
    marginBottom: 12,
  },
  removeBtn: {
    background: 'transparent',
    border: 'none',
    color: 'var(--red)',
    padding: '4px 0',
    fontSize: 12,
    cursor: 'pointer',
  },
  error: {
    fontFamily: "'JetBrains Mono', monospace",
    fontSize: 11,
    color: 'var(--red)',
    background: 'var(--red-dim)',
    padding: '8px 10px',
    borderRadius: 4,
    marginBottom: 12,
    overflowWrap: 'anywhere',
  },
  successMsg: {
    fontFamily: "'JetBrains Mono', monospace",
    fontSize: 11,
    color: 'var(--green)',
    marginBottom: 12,
  },
  providerMeta: {
    display: 'flex',
    alignItems: 'center',
    gap: 10,
    marginBottom: 10,
    minHeight: 22,
    flexWrap: 'wrap',
  },
  providerList: {
    marginTop: 16,
    borderTop: '1px solid var(--border)',
  },
  providerRow: {
    width: '100%',
    display: 'grid',
    gridTemplateColumns: 'minmax(0, 1fr) 120px 64px',
    gap: 8,
    alignItems: 'center',
    padding: '9px 0',
    border: 'none',
    borderBottom: '1px solid var(--border)',
    background: 'transparent',
    color: 'var(--text)',
    cursor: 'pointer',
    textAlign: 'left',
    outline: 'none',
  },
  providerRowActive: {
    width: '100%',
    display: 'grid',
    gridTemplateColumns: 'minmax(0, 1fr) 120px 64px',
    gap: 8,
    alignItems: 'center',
    padding: '9px 0 9px 8px',
    border: 'none',
    borderBottom: '1px solid var(--border)',
    borderLeft: '2px solid var(--cyan)',
    background: 'transparent',
    color: 'var(--text)',
    cursor: 'pointer',
    textAlign: 'left',
    outline: 'none',
  },
  providerName: {
    minWidth: 0,
    overflow: 'hidden',
    textOverflow: 'ellipsis',
    whiteSpace: 'nowrap',
    fontFamily: "'JetBrains Mono', monospace",
    fontSize: 12,
  },
  providerConfigured: {
    color: 'var(--green)',
    fontFamily: "'JetBrains Mono', monospace",
    fontSize: 11,
    whiteSpace: 'nowrap',
  },
  providerUnset: {
    color: 'var(--muted)',
    fontFamily: "'JetBrains Mono', monospace",
    fontSize: 11,
    whiteSpace: 'nowrap',
  },
  providerCount: {
    color: 'var(--muted)',
    fontFamily: "'JetBrains Mono', monospace",
    fontSize: 11,
    textAlign: 'right',
  },
  emptyText: {
    fontFamily: "'JetBrains Mono', monospace",
    fontSize: 11,
    color: 'var(--muted)',
    paddingTop: 14,
    display: 'block',
  },
  flowPanel: {
    borderTop: '1px solid var(--border)',
    borderBottom: '1px solid var(--border)',
    padding: '16px 0',
    margin: '16px 0 18px',
  },
  flowHeader: {
    display: 'flex',
    justifyContent: 'space-between',
    alignItems: 'flex-start',
    gap: 12,
    marginBottom: 12,
  },
  flowTitleGroup: {
    minWidth: 0,
    display: 'flex',
    flexDirection: 'column',
    gap: 4,
  },
  flowProviderName: {
    minWidth: 0,
    overflow: 'hidden',
    textOverflow: 'ellipsis',
    whiteSpace: 'nowrap',
    color: 'var(--text)',
    fontSize: 13,
    fontWeight: 500,
  },
  flowStatusPill: {
    border: '1px solid',
    borderRadius: 999,
    padding: '3px 8px',
    fontFamily: "'JetBrains Mono', monospace",
    fontSize: 10,
    lineHeight: 1.2,
    whiteSpace: 'nowrap',
  },
  loginRow: {
    display: 'flex',
    alignItems: 'center',
    gap: 12,
    flexWrap: 'wrap',
    marginBottom: 8,
  },
  loginLink: {
    background: 'var(--blue)',
    border: 'none',
    borderRadius: 4,
    color: '#fff',
    display: 'inline-flex',
    alignItems: 'center',
    minHeight: 34,
    padding: '8px 14px',
    fontSize: 13,
    fontWeight: 500,
    textDecoration: 'none',
  },
  waitingText: {
    color: 'var(--green)',
    fontFamily: "'JetBrains Mono', monospace",
    fontSize: 11,
  },
  flowText: {
    color: 'var(--muted)',
    fontSize: 12,
    lineHeight: 1.5,
    margin: '0 0 8px',
    overflowWrap: 'anywhere',
  },
  progressText: {
    fontFamily: "'JetBrains Mono', monospace",
    color: 'var(--muted)',
    fontSize: 11,
    marginBottom: 8,
  },
  fallbackBtn: {
    background: 'transparent',
    border: 'none',
    color: 'var(--cyan)',
    padding: 0,
    fontSize: 12,
    cursor: 'pointer',
  },
  flowActions: {
    display: 'flex',
    alignItems: 'center',
    gap: 16,
    flexWrap: 'wrap',
    marginTop: 10,
  },
  flowCancelBtn: {
    background: 'transparent',
    border: 'none',
    color: 'var(--red)',
    padding: 0,
    fontSize: 12,
    cursor: 'pointer',
  },
  flowInputRow: {
    display: 'grid',
    gridTemplateColumns: 'minmax(0, 1fr) auto',
    gap: 8,
    marginTop: 10,
  },
  section: {
    borderTop: '1px solid var(--border)',
    marginTop: 18,
    paddingTop: 18,
  },
  sectionHeader: {
    display: 'flex',
    alignItems: 'center',
    justifyContent: 'space-between',
    gap: 12,
  },
  sectionTitleGroup: {
    minWidth: 0,
    display: 'flex',
    alignItems: 'baseline',
    gap: 10,
    flexWrap: 'wrap',
  },
  iconBtn: {
    width: 30,
    height: 30,
    display: 'inline-flex',
    alignItems: 'center',
    justifyContent: 'center',
    flexShrink: 0,
    background: 'var(--bg)',
    border: '1px solid var(--border)',
    borderRadius: 4,
    color: 'var(--cyan)',
    fontSize: 19,
    lineHeight: 1,
    cursor: 'pointer',
    outline: 'none',
  },
  customEditor: {
    borderTop: '1px solid var(--border)',
    marginTop: 14,
    paddingTop: 14,
  },
  customGrid: {
    display: 'grid',
    gridTemplateColumns: 'repeat(auto-fit, minmax(180px, 1fr))',
    gap: 10,
  },
  fieldLabel: {
    display: 'flex',
    flexDirection: 'column',
    gap: 5,
    color: 'var(--muted)',
    fontSize: 11,
    fontFamily: "'JetBrains Mono', monospace",
    textTransform: 'uppercase',
  },
  fieldLabelLarge: {
    display: 'flex',
    flexDirection: 'column',
    gap: 5,
    color: 'var(--muted)',
    fontSize: 11,
    fontFamily: "'JetBrains Mono', monospace",
    textTransform: 'uppercase',
  },
  toggleRow: {
    display: 'flex',
    alignItems: 'center',
    gap: 10,
    flexWrap: 'wrap',
    marginTop: 12,
  },
  checkboxLabel: {
    display: 'flex',
    alignItems: 'center',
    gap: 8,
    color: 'var(--text)',
    fontSize: 13,
  },
  customProviderList: {
    marginTop: 14,
    borderTop: '1px solid var(--border)',
  },
  customProviderRow: {
    display: 'grid',
    gridTemplateColumns: 'minmax(0, 1fr) auto',
    gap: 10,
    alignItems: 'center',
    borderBottom: '1px solid var(--border)',
    padding: '8px 0',
  },
  customProviderPick: {
    minWidth: 0,
    display: 'grid',
    gridTemplateColumns: 'minmax(0, 1fr) 78px 80px',
    gap: 8,
    alignItems: 'center',
    border: 'none',
    background: 'transparent',
    color: 'var(--text)',
    textAlign: 'left',
    padding: 0,
    outline: 'none',
    cursor: 'pointer',
  },
};
