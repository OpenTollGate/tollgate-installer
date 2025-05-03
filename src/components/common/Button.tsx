import React from 'react';
import styled, { css } from 'styled-components';

type ButtonVariant = 'primary' | 'secondary' | 'outline' | 'text';
type ButtonSize = 'small' | 'medium' | 'large';

interface ButtonProps extends React.ButtonHTMLAttributes<HTMLButtonElement> {
  variant?: ButtonVariant;
  size?: ButtonSize;
  fullWidth?: boolean;
  children: React.ReactNode;
  isLoading?: boolean;
}

const StyledButton = styled.button<Omit<ButtonProps, 'children'>>`
  display: inline-flex;
  align-items: center;
  justify-content: center;
  gap: 0.5rem;
  font-weight: ${props => props.theme.fontWeights.medium};
  cursor: pointer;
  transition: all ${props => props.theme.transitions.fast};
  border-radius: ${props => props.theme.radii.md};
  white-space: nowrap;
  
  ${props => props.fullWidth && css`
    width: 100%;
  `}
  
  ${props => {
    // Different sizes
    switch (props.size) {
      case 'small':
        return css`
          padding: 0.5rem 1rem;
          font-size: ${props.theme.fontSizes.sm};
        `;
      case 'large':
        return css`
          padding: 0.75rem 1.5rem;
          font-size: ${props.theme.fontSizes.lg};
        `;
      case 'medium':
      default:
        return css`
          padding: 0.625rem 1.25rem;
          font-size: ${props.theme.fontSizes.md};
        `;
    }
  }}
  
  ${props => {
    // Different variants
    switch (props.variant) {
      case 'secondary':
        return css`
          background-color: ${props.theme.colors.textSecondary};
          color: ${props.theme.colors.textLight};
          
          &:hover, &:focus {
            background-color: ${props.theme.colors.text};
          }
          
          &:disabled {
            background-color: ${props.theme.colors.border};
            color: ${props.theme.colors.textSecondary};
            cursor: not-allowed;
          }
        `;
      case 'outline':
        return css`
          background-color: transparent;
          color: ${props.theme.colors.primary};
          border: 1px solid ${props.theme.colors.primary};
          
          &:hover, &:focus {
            background-color: ${props.theme.colors.primaryLight};
          }
          
          &:disabled {
            border-color: ${props.theme.colors.border};
            color: ${props.theme.colors.textSecondary};
            cursor: not-allowed;
          }
        `;
      case 'text':
        return css`
          background-color: transparent;
          color: ${props.theme.colors.primary};
          padding-left: 0.5rem;
          padding-right: 0.5rem;
          
          &:hover, &:focus {
            color: ${props.theme.colors.primaryDark};
            background-color: ${props.theme.colors.primaryLight};
          }
          
          &:disabled {
            color: ${props.theme.colors.textSecondary};
            cursor: not-allowed;
          }
        `;
      case 'primary':
      default:
        return css`
          background-color: ${props.theme.colors.primary};
          color: ${props.theme.colors.textLight};
          
          &:hover, &:focus {
            background-color: ${props.theme.colors.primaryDark};
          }
          
          &:disabled {
            background-color: ${props.theme.colors.border};
            color: ${props.theme.colors.textSecondary};
            cursor: not-allowed;
          }
        `;
    }
  }}
`;

const Button: React.FC<ButtonProps> = ({ 
  children, 
  variant = 'primary', 
  size = 'medium', 
  fullWidth = false,
  isLoading = false,
  disabled,
  ...props 
}) => {
  return (
    <StyledButton
      variant={variant}
      size={size}
      fullWidth={fullWidth}
      disabled={disabled || isLoading}
      {...props}
    >
      {isLoading ? 'Loading...' : children}
    </StyledButton>
  );
};

export default Button;