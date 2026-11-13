// Ordering the model catalogs by how current a model is. A hand-written list
// of mainstream models went stale the moment a release added one, and left new
// models below the ones they replace.

// Which vendor leads a generation, once the newest models are already first.
// It decides nothing about how new a model is.
const providerFamilyOrder = [
  'openai',
  'anthropic',
  'claude',
  'google',
  'gemini',
  'deepseek',
  'qwen',
  'moonshot',
  'kimi',
  'glm',
  'seedance',
] as const;

const modalityRank: Record<string, number> = {
  text: 0,
  embedding: 1,
  image: 2,
  video: 3,
};

export interface CatalogOrderItem {
  model_name: string;
  provider_family?: string;
  // The public price list does not publish a modality; everything there is
  // ranked on one line per vendor instead.
  modality?: string;
}

// A public name carries its generation: zt-gpt-6-astra is newer than
// zt-gpt-5.6-sol, and zt-kimi-k3 newer than zt-kimi-k2.7-code. A name with no
// number in it sorts behind every generation its vendor does have.
export function modelGeneration(modelName: string) {
  const version = /\d+(?:\.\d+)?/.exec(modelName.replace(/^zt-/, ''));
  return version ? Number.parseFloat(version[0]) : Number.NEGATIVE_INFINITY;
}

function catalogLine(item: CatalogOrderItem) {
  return `${(item.provider_family ?? '').toLocaleLowerCase()}/${item.modality ?? 'text'}`;
}

function generationKey(item: CatalogOrderItem) {
  return `${catalogLine(item)}/${modelGeneration(item.model_name)}`;
}

function descending(left: number, right: number) {
  return right === left ? 0 : right > left ? 1 : -1;
}

// Rank each generation within its own vendor and modality, because a version
// number only means something next to the same vendor's other models: Kimi K3
// is current, GPT 4.1 is not, and the larger number is the older model.
export function generationRanks(catalog: readonly CatalogOrderItem[]) {
  const versionsByLine = new Map<string, Set<number>>();
  for (const item of catalog) {
    const line = catalogLine(item);
    const versions = versionsByLine.get(line) ?? new Set<number>();
    versions.add(modelGeneration(item.model_name));
    versionsByLine.set(line, versions);
  }

  const ranks = new Map<string, number>();
  for (const [line, versions] of versionsByLine) {
    [...versions]
      .sort(descending)
      .forEach((version, index) => ranks.set(`${line}/${version}`, index));
  }
  return ranks;
}

export function providerFamilyRank(family: string | undefined) {
  const index = providerFamilyOrder.indexOf(
    (family ?? '').toLocaleLowerCase() as (typeof providerFamilyOrder)[number],
  );
  return index === -1 ? providerFamilyOrder.length : index;
}

export function compareCatalogRecency<T extends CatalogOrderItem>(
  ranks: Map<string, number>,
) {
  return (left: T, right: T) => {
    const leftRank = ranks.get(generationKey(left)) ?? Number.MAX_SAFE_INTEGER;
    const rightRank = ranks.get(generationKey(right)) ?? Number.MAX_SAFE_INTEGER;
    return (
      leftRank - rightRank ||
      (modalityRank[left.modality ?? 'text'] ?? 0) -
        (modalityRank[right.modality ?? 'text'] ?? 0) ||
      providerFamilyRank(left.provider_family) - providerFamilyRank(right.provider_family) ||
      descending(modelGeneration(left.model_name), modelGeneration(right.model_name)) ||
      left.model_name.localeCompare(right.model_name)
    );
  };
}

// One list mixing every vendor leads with the newest model each of them sells,
// whatever that model does.
export function sortCatalogByRecency<T extends CatalogOrderItem>(catalog: readonly T[]) {
  return [...catalog].sort(compareCatalogRecency<T>(generationRanks(catalog)));
}

// One vendor's own table reads by what a model does first, so its chat models
// stay together instead of being split apart by an image or embedding model
// that happens to be the newest of its kind.
export function sortVendorModelsByRecency<T extends CatalogOrderItem>(models: readonly T[]) {
  const ranks = generationRanks(models);
  const byRecency = compareCatalogRecency<T>(ranks);
  return [...models].sort(
    (left, right) =>
      (modalityRank[left.modality ?? 'text'] ?? 0) -
        (modalityRank[right.modality ?? 'text'] ?? 0) || byRecency(left, right),
  );
}
