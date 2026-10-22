// Hand-written declarations for generate-icons.mjs, which the icons unit
// test imports to re-derive the generated data from node_modules.
export declare const ICONS: readonly string[];
export declare function innerSvg(svg: string): string;
export declare function iconEntries(): Promise<Array<[string, string]>>;
