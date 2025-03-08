# Shopware plugin metadata

The local `shopware_extensions.json` file is updated every 30 minutes by a github action to refelct the current metadata of all available plugins in the packagist registry.

You can use this URL to download it: https://raw.githubusercontent.com/nuonic-digital/plugin-data/refs/heads/dev/shopware_extensions_v1.json

As this is WIP, keep in mind the URL can change any time.

## Want to get listed here?

Plugins available in the public https://packagist.org registry fulfilling the following criteria are listed:

- `"type": "shopware-platform-plugin",` in `composer.json`
- contain a `.shopware-extension.yml` in the repo root
