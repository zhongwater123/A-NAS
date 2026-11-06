const { getDefaultConfig } = require("expo/metro-config");
const path = require("node:path");
const config = getDefaultConfig(__dirname);
config.watchFolders = [path.resolve(__dirname, "..", "web", "src")];
config.resolver.nodeModulesPaths = [path.resolve(__dirname, "node_modules")];
// Hermes vendors ESM .js specifiers in TypeScript sources. Metro does not
// perform TypeScript's extension substitution; keep the upstream files intact.
config.resolver.resolveRequest = (context, name, platform) => {
  if (context.originModulePath.includes(`${path.sep}hermes${path.sep}shared${path.sep}`) && name.startsWith('./') && name.endsWith('.js')) name = name.slice(0, -3);
  return context.resolveRequest(context, name, platform);
};
module.exports = config;
