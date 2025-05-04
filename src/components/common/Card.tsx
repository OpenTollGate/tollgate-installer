import React from 'react';
import styled from 'styled-components';

interface CardProps {
  children: React.ReactNode;
  title?: string;
  subtitle?: string;
  fullWidth?: boolean;
  footer?: React.ReactNode;
}

const CardContainer = styled.div<{ fullWidth?: boolean }>`
  background-color: transparent;
  border-radius: ${props => props.theme.radii.lg};
  overflow: hidden;
  display: flex;
  flex-direction: column;
  width: ${props => props.fullWidth ? '100%' : '550px'};
  max-width: 100%;
  margin: 0 auto;
  padding: ${props => props.theme.space.xl};
`;

const CardHeader = styled.div`
  padding: ${props => props.theme.space.xl} ${props => props.theme.space.xl} ${props => props.theme.space.md};
  text-align: center;
`;

const CardTitle = styled.h2`
  margin-bottom: ${props => props.theme.space.xs};
  color: ${props => props.theme.colors.text};
  font-size: ${props => props.theme.fontSizes['2xl']};
  font-weight: ${props => props.theme.fontWeights.bold};
`;

const CardSubtitle = styled.p`
  color: ${props => props.theme.colors.textSecondary};
  font-size: ${props => props.theme.fontSizes.lg};
  margin-bottom: ${props => props.theme.space.md};
  max-width: 80%;
  margin-left: auto;
  margin-right: auto;
`;

const CardContent = styled.div`
  padding: ${props => props.theme.space.xl};
  flex: 1;
  display: flex;
  flex-direction: column;
  align-items: center;
`;

const CardFooter = styled.div`
  padding: ${props => props.theme.space.lg} ${props => props.theme.space.xl};
  display: flex;
  justify-content: center;
  gap: ${props => props.theme.space.md};
`;

const Card: React.FC<CardProps> = ({ 
  children, 
  title, 
  subtitle, 
  fullWidth = false,
  footer 
}) => {
  return (
    <CardContainer fullWidth={fullWidth}>
      {(title || subtitle) && (
        <CardHeader>
          {title && <CardTitle>{title}</CardTitle>}
          {subtitle && <CardSubtitle>{subtitle}</CardSubtitle>}
        </CardHeader>
      )}
      <CardContent>{children}</CardContent>
      {footer && <CardFooter>{footer}</CardFooter>}
    </CardContainer>
  );
};

export default Card;