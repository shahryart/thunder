// rollup.config.js
import typescript from "rollup-plugin-typescript";
import babel from "rollup-plugin-babel";
import resolve from "rollup-plugin-node-resolve";
import commonjs from "rollup-plugin-commonjs";
import tsc from "typescript";

export default {
  input: "./src/index.ts",

  output: {
    file: "lib/index.js",
    format: "cjs",
  },

  plugins: [
    resolve({
      only: ["babel-polyfill"],
    }),
    commonjs(),
    typescript({
      typescript: tsc,
    }),
    babel({
      exclude: "node_modules/**",
      runtimeHelpers: true,
    }),
  ],
};
