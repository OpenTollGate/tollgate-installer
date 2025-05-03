import 'styled-components';
import { theme } from './theme';

declare module 'styled-components' {
  export interface DefaultTheme {
    colors: typeof theme.colors;
    fonts: typeof theme.fonts;
    fontSizes: typeof theme.fontSizes;
    fontWeights: typeof theme.fontWeights;
    space: typeof theme.space;
    radii: typeof theme.radii;
    shadows: typeof theme.shadows;
    transitions: typeof theme.transitions;
  }
}