// SPDX-License-Identifier: AGPL-3.0-only
// Source-derived extract of owned WalletProductOptions, WalletProductService
// GetGoldsResupplyPackDefinition, and WalletProductController.GetCatalog at
// DysonNetwork 4ee2d0cebb9da8284a92483fee6dce795bf6947c. Modified 2026-10-08.
// The excluded Shared import is removed. Its constant reference is replaced
// ONLY with an explicitly declared reference-fixture currency. This does not
// verify the excluded constant or establish a canonical upstream default.

public static class FixtureDefaults
{
    public const string Currency = "golds";
}

public class ProductOptions
{
    public WalletProductDefinitionOptions GoldCurrency { get; set; } = new();
}

public class WalletProductDefinitionOptions
{
    public string Identifier { get; set; } = "wallet.golds_resupply_pack";
    public string DisplayName { get; set; } = "Golds Resupply Pack";
    public string Currency { get; set; } = FixtureDefaults.Currency;
    public Dictionary<string, Dictionary<string, decimal>> ProviderMappings { get; set; } = [];
}

public class WalletProductCatalogItem
{
    public string Key { get; set; } = null!;
    public string Identifier { get; set; } = null!;
    public string DisplayName { get; set; } = null!;
    public string Currency { get; set; } = null!;
    public Dictionary<string, Dictionary<string, decimal>> ProviderMappings { get; set; } = [];
}

public static class OwnedProjection
{
    public const string GoldsResupplyPackKey = "golds_resupply_pack";
    public static WalletProductCatalogItem Catalog(WalletProductDefinitionOptions definition) => new()
    {
        Key = GoldsResupplyPackKey,
        Identifier = definition.Identifier,
        DisplayName = definition.DisplayName,
        Currency = definition.Currency,
        ProviderMappings = definition.ProviderMappings
            .ToDictionary(
                kv => kv.Key,
                kv => kv.Value.ToDictionary(inner => inner.Key, inner => inner.Value, StringComparer.OrdinalIgnoreCase),
                StringComparer.OrdinalIgnoreCase
            )
    };
}
