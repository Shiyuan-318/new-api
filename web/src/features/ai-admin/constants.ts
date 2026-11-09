export const AI_ADMIN_CHAT_ENDPOINT = '/pg/chat/completions'
export const AI_ADMIN_TOOLS_ENDPOINT = '/api/ai-admin/tools'
export const AI_ADMIN_EXECUTE_ENDPOINT = '/api/ai-admin/execute'

export const DEFAULT_GROUP = 'default'

/** English keys are also the i18n source keys; zh translations live in locale files. */
export const TOOL_LABEL_KEYS: Record<string, string> = {
  get_model_price: 'Query Model Price',
  update_model_price: 'Update Model Price',
  create_channel: 'Create Channel',
  adjust_user_quota: 'Adjust User Balance',
  get_homepage_html: 'Read Homepage HTML',
  update_homepage_html: 'Write Homepage HTML',
}

export const TOOL_DESC_KEYS: Record<string, string> = {
  get_model_price: 'Query Model Price Description',
  update_model_price: 'Update Model Price Description',
  create_channel: 'Create Channel Description',
  adjust_user_quota: 'Adjust User Balance Description',
  get_homepage_html: 'Read Homepage HTML Description',
  update_homepage_html: 'Write Homepage HTML Description',
}

export const EXAMPLE_PROMPTS = [
  'What is the current price of gpt-4o?',
  'Set the ratio of gpt-4o to 2.5',
  'Add 5000 quota to user alice',
  'Create an OpenAI channel named test with models gpt-4o',
  'Show me the current homepage HTML',
  'Write a simple welcome page for the homepage',
]
