-- Seed data for MCPProvider
INSERT INTO "MCPProvider" ("id", "name", "description", "website", "email", "imageUrl", "serverUrl", "createdAt", "updatedAt")
VALUES
  ('1', 'OpenAI Provider', 'Access to OpenAI models including GPT-4 and DALL-E', 'https://openai.com', 'contact@openai.com', 'https://example.com/openai.png', 'https://api.openai.com/v1', NOW(), NOW()),
  ('2', 'Anthropic Provider', 'Access to Claude models for safe and helpful AI assistants', 'https://anthropic.com', 'contact@anthropic.com', 'https://example.com/anthropic.png', 'https://api.anthropic.com', NOW(), NOW()),
  ('3', 'Stability AI Provider', 'Access to Stable Diffusion models for image generation', 'https://stability.ai', 'contact@stability.ai', 'https://example.com/stability.png', 'https://api.stability.ai', NOW(), NOW()),
  ('4', 'My Custom Provider', 'A custom MCP server for internal use', null, null, null, 'https://api.mycustomserver.com', NOW(), NOW());

-- Seed data for MCPFunction
INSERT INTO "MCPFunction" ("id", "name", "description", "serverId", "createdAt", "updatedAt")
VALUES
  -- OpenAI functions
  ('1', 'chat', 'Chat completion API for generating conversational responses', '1', NOW(), NOW()),
  ('2', 'embeddings', 'Text embeddings API for converting text to vector representations', '1', NOW(), NOW()),
  ('3', 'image', 'Image generation API for creating images from text prompts', '1', NOW(), NOW()),
  ('4', 'audio', 'Audio transcription API', '1', NOW(), NOW()),
  
  -- Anthropic functions
  ('5', 'messages', 'Claude messaging API for generating conversational responses', '2', NOW(), NOW()),
  
  -- Stability AI functions
  ('6', 'text-to-image', 'Generate images from text descriptions', '3', NOW(), NOW()),
  ('7', 'image-to-image', 'Edit existing images using text prompts', '3', NOW(), NOW()),
  
  -- Custom server functions
  ('8', 'custom-function-1', 'Custom Function 1', '4', NOW(), NOW()),
  ('9', 'custom-function-2', 'Custom Function 2', '4', NOW(), NOW());

-- Seed data for FunctionParameter
INSERT INTO "FunctionParameter" ("id", "name", "type", "description", "functionId")
VALUES
  -- Chat parameters
  ('1', 'model', 'string', 'ID of the model to use', '1'),
  ('2', 'messages', 'array', 'Array of messages in the conversation', '1'),
  ('3', 'temperature', 'number', 'Sampling temperature between 0 and 2', '1'),
  
  -- Embeddings parameters
  ('4', 'model', 'string', 'ID of the model to use', '2'),
  ('5', 'input', 'string', 'Input text to get embeddings for', '2'),
  
  -- Image parameters
  ('6', 'prompt', 'string', 'Text description of the desired image', '3'),
  ('7', 'n', 'integer', 'Number of images to generate', '3'),
  ('8', 'size', 'string', 'Size of the generated images', '3'),
  
  -- Messages parameters (Anthropic)
  ('9', 'model', 'string', 'ID of the model to use', '5'),
  ('10', 'messages', 'array', 'Array of messages in the conversation', '5'),
  ('11', 'temperature', 'number', 'Sampling temperature between 0 and 1', '5'),
  
  -- Text-to-image parameters (Stability)
  ('12', 'engine_id', 'string', 'Stable Diffusion version to use', '6'),
  ('13', 'prompt', 'string', 'Text prompt for image generation', '6'),
  ('14', 'cfg_scale', 'number', 'Prompt guidance scale', '6'),
  
  -- Image-to-image parameters (Stability)
  ('15', 'engine_id', 'string', 'Stable Diffusion version to use', '7'),
  ('16', 'prompt', 'string', 'Text prompt for image editing', '7'),
  ('17', 'init_image', 'string', 'Base64 encoded image to edit', '7');

-- Seed a demo user with password 'password'
INSERT INTO "User" ("id", "email", "password", "name", "createdAt", "updatedAt", "isAdmin")
VALUES
  ('1', 'demo@example.com', '$2b$10$dGO8/dDfyeEcSOhT4xQWh.OtCJWWfuT1HYLJ4bN/X1pJ4P2IhjfK2', 'Demo User', NOW(), NOW(), false);

-- Seed API keys
INSERT INTO "ApiKey" ("id", "name", "key", "userId", "createdAt", "updatedAt", "lastUsed")
VALUES
  ('1', 'Development Key', 'mp_dev_1234567890abcdef1234567890abcdef', '1', NOW(), NOW(), '2023-03-20T15:30:00Z'),
  ('2', 'Production Key', 'mp_prod_abcdef1234567890abcdef1234567890', '1', NOW(), NOW(), '2023-03-25T18:45:00Z');

-- Seed subscriptions
INSERT INTO "Subscription" ("id", "userId", "serverId", "createdAt")
VALUES
  ('1', '1', '1', NOW()),
  ('2', '1', '2', NOW());

-- Seed function subscriptions
INSERT INTO "Subscription" ("id", "userId", "functionId", "createdAt")
VALUES
  ('3', '1', '1', NOW()),
  ('4', '1', '2', NOW()),
  ('5', '1', '5', NOW());

-- Seed the custom server as owned by demo user
INSERT INTO "_MCPProviderToUser" ("A", "B")
VALUES
  ('4', '1'); 