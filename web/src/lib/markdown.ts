import { defaultUrlTransform, type Options, type UrlTransform } from "react-markdown";
import remarkGfm from "remark-gfm";
import { remarkStrong } from "./strong";

export const markdownOptions = {
  remarkPlugins: [remarkGfm, remarkStrong],
  skipHtml: true,
  urlTransform: defaultUrlTransform,
} satisfies Options;

// Board and Task links open outside the current view. Transcript links can also point inside Dex.
export const webLinksOnly: UrlTransform = (url, key) =>
  key === "href" && !/^https?:\/\//i.test(url) ? undefined : markdownOptions.urlTransform(url);
