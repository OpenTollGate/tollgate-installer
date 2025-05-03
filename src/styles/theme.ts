// Theme configuration for the TollGate Installer
// These colors should match the TollGate captive portal style

export const theme = {
  colors: {
    // Primary colors
    primary: '#F97316', // Orange
    primaryDark: '#EA580C',
    primaryLight: '#FFEDD5',
    
    // Text colors
    text: '#1F2937', // Dark gray for main text
    textSecondary: '#6B7280', // Medium gray for secondary text
    textLight: '#FFFFFF', // White text for buttons, etc.
    
    // Background colors
    background: '#FFFFFF',
    backgroundAlt: '#F3F4F6',
    
    // Status colors
    success: '#10B981', // Green
    error: '#EF4444',   // Red
    warning: '#F59E0B', // Amber
    info: '#3B82F6',    // Blue
    
    // Border colors
    border: '#E5E7EB',
    borderFocus: '#F97316'
  },
  
  // Typography
  fonts: {
    body: '-apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, Helvetica, Arial, sans-serif',
    heading: '-apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, Helvetica, Arial, sans-serif',
  },
  
  // Font sizes
  fontSizes: {
    xs: '0.75rem',
    sm: '0.875rem',
    md: '1rem',
    lg: '1.125rem',
    xl: '1.25rem',
    '2xl': '1.5rem',
    '3xl': '1.875rem',
    '4xl': '2.25rem'
  },
  
  // Font weights
  fontWeights: {
    normal: 400,
    medium: 500,
    semibold: 600,
    bold: 700
  },
  
  // Spacing scale
  space: {
    xs: '0.25rem',
    sm: '0.5rem',
    md: '1rem',
    lg: '1.5rem',
    xl: '2rem',
    '2xl': '3rem',
    '3xl': '4rem'
  },
  
  // Border radius
  radii: {
    sm: '0.25rem',
    md: '0.375rem',
    lg: '0.5rem',
    full: '9999px'
  },
  
  // Shadows
  shadows: {
    sm: '0 1px 2px 0 rgba(0, 0, 0, 0.05)',
    md: '0 4px 6px -1px rgba(0, 0, 0, 0.1), 0 2px 4px -1px rgba(0, 0, 0, 0.06)',
    lg: '0 10px 15px -3px rgba(0, 0, 0, 0.1), 0 4px 6px -2px rgba(0, 0, 0, 0.05)'
  },
  
  // Animations
  transitions: {
    fast: '150ms ease-in-out',
    normal: '300ms ease-in-out',
    slow: '500ms ease-in-out'
  }
};