"use client";

import { Plug, Shuffle, Zap } from "lucide-react";
import type { FormEvent, ReactNode } from "react";

import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { pageTitleClass } from "@/lib/utils";

type RouterCategory = { name: string; description: string; model: string };
type ModelOption = {
  id: string;
  provider_id?: string;
  display_name?: string;
  reasoning?: boolean;
};

/**
 * One settings section. The three panels used to be modals, which suited a
 * one-off "paste a key and go" interaction but fought everything else: the
 * router form is tall enough to scroll inside a dialog, and comparing the
 * provider against the router's classifier meant closing one to open the other.
 * On a page they are readable side by side and can be linked to.
 */
function Section({
  id,
  icon,
  title,
  description,
  children,
}: {
  id: string;
  icon: ReactNode;
  title: string;
  description: string;
  children: ReactNode;
}) {
  return (
    <section
      id={id}
      aria-labelledby={`${id}-heading`}
      className="settings-section settings-section--tabbed"
    >
      <header className="settings-section-head">
        <span className="settings-section-icon">{icon}</span>
        <div>
          <h2 id={`${id}-heading`}>{title}</h2>
          <p>{description}</p>
        </div>
      </header>
      <div className="settings-section-body">{children}</div>
    </section>
  );
}

export function SettingsView({
  controllerMode,
  providerName,
  authFile,
  routerState,
  routerError,
  routerManagedBy,
  routerEnabled,
  setRouterEnabled,
  routerClassifierModel,
  setRouterClassifierModel,
  routerDefaultCategory,
  setRouterDefaultCategory,
  routerCategories,
  routerModels,
  addRouterCategory,
  removeRouterCategory,
  renameRouterCategory,
  updateRouterCategory,
  saveModelRouter,
  mcpName,
  setMcpName,
  mcpUrl,
  setMcpUrl,
  mcpToken,
  setMcpToken,
  mcpState,
  mcpError,
  mcpConnected,
  connectMcp,
  signInToMcp,
}: {
  controllerMode: "managed" | "external";
  providerName: string;
  authFile: string;
  routerState: "idle" | "loading" | "saving" | "success" | "error";
  routerError: string;
  routerManagedBy: "studio" | "operator-settings";
  routerEnabled: boolean;
  setRouterEnabled: (value: boolean) => void;
  routerClassifierModel: string;
  setRouterClassifierModel: (value: string) => void;
  routerDefaultCategory: string;
  setRouterDefaultCategory: (value: string) => void;
  routerCategories: RouterCategory[];
  routerModels: ModelOption[];
  addRouterCategory: () => void;
  removeRouterCategory: (index: number) => void;
  renameRouterCategory: (index: number, name: string) => void;
  updateRouterCategory: (index: number, patch: Partial<RouterCategory>) => void;
  saveModelRouter: (event: FormEvent) => void;
  mcpName: string;
  setMcpName: (value: string) => void;
  mcpUrl: string;
  setMcpUrl: (value: string) => void;
  mcpToken: string;
  setMcpToken: (value: string) => void;
  mcpState: "idle" | "saving" | "success" | "error";
  mcpError: string;
  mcpConnected: { name: string; url: string } | null;
  connectMcp: (event: FormEvent) => void;
  signInToMcp: () => void | Promise<void>;
}) {
  const external = controllerMode === "external";

  return (
    <div className="settings-page">
      <header className="settings-page-head">
        <h1 className={pageTitleClass("text-4xl leading-tight")}>Settings</h1>
        <p>
          How this Studio reaches a model, how it routes delegations, and which
          external tools it can use.
        </p>
      </header>

      <Tabs defaultValue="provider" className="gap-0">
        <TabsList className="mb-6">
          <TabsTrigger value="provider">Provider</TabsTrigger>
          <TabsTrigger value="router">Model router</TabsTrigger>
          <TabsTrigger value="mcp">MCP gateway</TabsTrigger>
        </TabsList>

        <TabsContent value="provider">
          <Section
            id="provider"
            icon={<Zap className="size-[18px]" />}
            title="Provider"
            description="The model provider bound to every session this Studio opens."
          >
            <div className="gateway-status">
              <span>●</span>
              <p>
                <strong>{providerName}</strong>
                <small>
                  {external
                    ? "Owned by the external mecated deployment"
                    : "Managed local daemon"}
                </small>
              </p>
            </div>
            {external ? (
              <div className="transport-note">
                <span>i</span>
                <p>
                  Provider selection and credentials stay with the remote
                  daemon. Studio only receives the model inventory that
                  deployment exposes.
                </p>
              </div>
            ) : (
              <>
                <p className="settings-copy">
                  Studio never accepts or forwards provider secrets. Configure
                  mecated&rsquo;s conventional credentials file, then restart
                  Studio.
                </p>
                {authFile && <code className="skills-path">{authFile}</code>}
                <div className="key-safety">
                  <span>✓</span>
                  <p>
                    Set <code>MECATL_STUDIO_PROVIDER=openrouter</code> to select
                    OpenRouter; put its API key under{" "}
                    <code>providers.openrouter.api_key</code> in the auth file.
                    A missing credential fails loudly instead of falling back.
                  </p>
                </div>
              </>
            )}
          </Section>
        </TabsContent>

        <TabsContent value="router">
          <Section
            id="router"
            icon={<Shuffle className="size-[18px]" />}
            title="Model router"
            description="Classify each unpinned delegation by intent, then run it on the gateway model assigned to that category."
          >
            {external ? (
              <div className="transport-note">
                <span>i</span>
                <p>Routing is managed by the external mecated deployment.</p>
              </div>
            ) : routerState === "loading" ? (
              <div className="router-loading">
                Loading gateway models and routing settings…
              </div>
            ) : (
              <form onSubmit={saveModelRouter}>
                {routerManagedBy === "operator-settings" && (
                  <div className="router-managed" role="status">
                    <span>✓</span>
                    <p>
                      <strong>Imported operator policy is active</strong>
                      <small>
                        This complete configuration also controls aliases, model
                        slots, and guardrails. Router-only editing is locked to
                        keep those settings intact.
                      </small>
                    </p>
                  </div>
                )}
                <fieldset
                  className="router-managed-fields"
                  disabled={routerManagedBy === "operator-settings"}
                >
                  <label className="router-switch">
                    <input
                      type="checkbox"
                      aria-label="Enable semantic routing"
                      checked={routerEnabled}
                      onChange={(event) =>
                        setRouterEnabled(event.target.checked)
                      }
                    />
                    <span>
                      <strong>Semantic routing enabled</strong>
                      <small>
                        The taxonomy enables routing. Turn this off to keep it
                        configured but inactive.
                      </small>
                    </span>
                  </label>
                  <div className="router-grid">
                    <div>
                      <label htmlFor="router-classifier">
                        Classifier model
                      </label>
                      <input
                        id="router-classifier"
                        list="router-model-options"
                        value={routerClassifierModel}
                        onChange={(event) =>
                          setRouterClassifierModel(event.target.value)
                        }
                        placeholder="claude-haiku-4-5"
                      />
                    </div>
                    <div>
                      <label htmlFor="router-default">Default category</label>
                      <select
                        id="router-default"
                        value={routerDefaultCategory}
                        onChange={(event) =>
                          setRouterDefaultCategory(event.target.value)
                        }
                      >
                        {routerCategories.map((category, index) => (
                          <option
                            key={`${category.name}-${index}`}
                            value={category.name}
                          >
                            {category.name || `Category ${index + 1}`}
                          </option>
                        ))}
                      </select>
                    </div>
                  </div>
                  <div className="input-hint">
                    The classifier is one small extra call per routable
                    delegation. Explicit model choices still take precedence.
                  </div>
                  <datalist id="router-model-options">
                    {routerModels.map((model) => (
                      <option key={model.id} value={model.id}>
                        {model.display_name || model.id}
                      </option>
                    ))}
                  </datalist>
                  <div className="router-section-heading">
                    <span>Routing categories</span>
                    <button
                      type="button"
                      onClick={addRouterCategory}
                      disabled={routerCategories.length >= 8}
                    >
                      ＋ Add category
                    </button>
                  </div>
                  <div className="router-categories">
                    {routerCategories.map((category, index) => (
                      <fieldset className="router-category" key={index}>
                        <legend>Category {index + 1}</legend>
                        <button
                          className="router-remove"
                          type="button"
                          onClick={() => removeRouterCategory(index)}
                          disabled={routerCategories.length <= 2}
                          aria-label={`Remove category ${index + 1}`}
                        >
                          ×
                        </button>
                        <div className="router-grid">
                          <div>
                            <label htmlFor={`router-name-${index}`}>Name</label>
                            <input
                              id={`router-name-${index}`}
                              value={category.name}
                              onChange={(event) =>
                                renameRouterCategory(
                                  index,
                                  event.target.value
                                    .toLowerCase()
                                    .replace(/[^a-z0-9_-]/g, ""),
                                )
                              }
                              placeholder="routine"
                            />
                          </div>
                          <div>
                            <label htmlFor={`router-model-${index}`}>
                              Gateway model
                            </label>
                            <input
                              id={`router-model-${index}`}
                              list="router-model-options"
                              value={category.model}
                              onChange={(event) =>
                                updateRouterCategory(index, {
                                  model: event.target.value,
                                })
                              }
                              placeholder="claude-sonnet-5"
                            />
                          </div>
                        </div>
                        <label htmlFor={`router-description-${index}`}>
                          Classifier description
                        </label>
                        <textarea
                          id={`router-description-${index}`}
                          value={category.description}
                          onChange={(event) =>
                            updateRouterCategory(index, {
                              description: event.target.value,
                            })
                          }
                          maxLength={300}
                          placeholder="Describe the tasks that belong in this category. Keep categories distinct."
                        />
                      </fieldset>
                    ))}
                  </div>
                  <div className="key-safety router-safety">
                    <span>i</span>
                    <p>
                      Routing applies to plain Subagents, unpinned agent
                      definitions, team members, and parallel branches. It never
                      changes the Provider bound to a Session.
                    </p>
                  </div>
                </fieldset>
                {routerState === "error" && (
                  <div className="credential-error" role="alert">
                    {routerError}
                  </div>
                )}
                <button
                  className={`connect-button ${routerState}`}
                  type="submit"
                  disabled={
                    routerManagedBy === "operator-settings" ||
                    routerState === "saving" ||
                    routerCategories.length < 2 ||
                    !routerClassifierModel.trim() ||
                    !routerDefaultCategory ||
                    routerCategories.some(
                      (category) =>
                        !category.name.trim() ||
                        !category.description.trim() ||
                        !category.model.trim(),
                    )
                  }
                >
                  {routerManagedBy === "operator-settings"
                    ? "Managed by imported settings"
                    : routerState === "saving"
                      ? "Restarting Mecatl with router…"
                      : routerState === "success"
                        ? "Routing configured ✓"
                        : "Save routing and restart Mecatl"}
                </button>
              </form>
            )}
          </Section>
        </TabsContent>

        <TabsContent value="mcp">
          <Section
            id="mcp"
            icon={<Plug className="size-[18px]" />}
            title="MCP gateway"
            description="Add a Streamable HTTP gateway. Mecatl discovers its tools, resources, and prompts and offers them to your tasks."
          >
            {external ? (
              <div className="transport-note">
                <span>i</span>
                <p>
                  Gateway connections are managed by the external deployment.
                </p>
              </div>
            ) : (
              <>
                {mcpConnected && (
                  <div className="gateway-status">
                    <span>●</span>
                    <p>
                      <strong>Connected</strong>
                      <small>
                        {mcpConnected.name} · {mcpConnected.url}
                      </small>
                    </p>
                  </div>
                )}
                <form onSubmit={connectMcp}>
                  <div className="field-row">
                    <div>
                      <label htmlFor="mcp-name">Gateway name</label>
                      <input
                        id="mcp-name"
                        value={mcpName}
                        onChange={(event) =>
                          setMcpName(
                            event.target.value.replace(/[^A-Za-z0-9_]/g, ""),
                          )
                        }
                        placeholder="gateway"
                      />
                    </div>
                    <div className="url-field">
                      <label htmlFor="mcp-url">Streamable HTTP URL</label>
                      <input
                        id="mcp-url"
                        type="url"
                        value={mcpUrl}
                        onChange={(event) => setMcpUrl(event.target.value)}
                        placeholder="https://gateway.example.com/mcp"
                      />
                    </div>
                  </div>
                  <button
                    className={`connect-button ${mcpState}`}
                    type="button"
                    disabled={
                      !mcpName.trim() || !mcpUrl.trim() || mcpState === "saving"
                    }
                    onClick={() => void signInToMcp()}
                  >
                    {mcpState === "saving"
                      ? "Waiting for gateway sign-in…"
                      : mcpState === "success"
                        ? "Gateway connected ✓"
                        : "Sign in to Gateway"}
                  </button>
                  <div className="input-hint">
                    Uses the gateway&rsquo;s OAuth sign-in with PKCE. No
                    password or access token is stored by the browser.
                  </div>
                  <label htmlFor="mcp-token">
                    Existing bearer token{" "}
                    <span className="optional">Advanced</span>
                  </label>
                  <input
                    id="mcp-token"
                    type="password"
                    value={mcpToken}
                    onChange={(event) => setMcpToken(event.target.value)}
                    placeholder="Paste the gateway access token"
                    autoComplete="off"
                  />
                  <div className="input-hint">
                    Only use this when your gateway administrator supplied a
                    current access token.
                  </div>
                  <div className="input-hint">
                    Gateway URLs must use HTTPS. A local HTTP gateway is
                    accepted only when the controller is explicitly started with{" "}
                    <code>MECATL_ALLOW_INSECURE_LOOPBACK_MCP=1</code>.
                  </div>
                  <div className="key-safety">
                    <span>✓</span>
                    <p>
                      Credentials stay in the loopback controller&rsquo;s memory
                      and are never stored in this app or repository.
                    </p>
                  </div>
                  {mcpState === "error" && (
                    <div className="credential-error" role="alert">
                      {mcpError || "The gateway rejected the connection."}
                    </div>
                  )}
                  <button
                    className="connect-button secondary-connect"
                    type="submit"
                    disabled={
                      !mcpName.trim() ||
                      !mcpUrl.trim() ||
                      !mcpToken.trim() ||
                      mcpState === "saving"
                    }
                  >
                    Connect with existing token
                  </button>
                </form>
                <div className="transport-note">
                  <span>i</span>
                  <p>
                    Mecatl supports the MCP Streamable HTTP transport. Stdio and
                    legacy SSE gateways are not supported.
                  </p>
                </div>
              </>
            )}
          </Section>
        </TabsContent>
      </Tabs>
    </div>
  );
}
